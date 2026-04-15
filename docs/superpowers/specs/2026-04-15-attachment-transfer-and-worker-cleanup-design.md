# Attachment Transfer via Redis & Worker Cleanup

## Summary

Two problems, one spec:

1. **Attachment transfer**: App downloads Slack attachments locally, but Worker runs on a different machine (colleague's laptop or k8s pod). Files need to travel App → Redis → Worker.
2. **Worker cleanup**: RepoCache is designed as a persistent cache. Workers on ephemeral machines (colleague laptops, pods) need aggressive cleanup — after each job and on shutdown/crash.

## Motivation

### Attachment transfer
Current state: App downloads files to `/tmp/triage-meta-*`, writes local paths into the prompt, stores only metadata (filename + empty URL) in Redis. Worker calls `Resolve()`, gets metadata back, but has no way to access the actual files. The "copy attachments" loop in `executor.go` is a no-op.

### Worker cleanup
`RepoCache` clones repos and never deletes them. On a colleague's laptop running as a worker, this means repos accumulate forever. There's no cleanup on job completion, no cleanup on shutdown, and no cleanup on next startup after a crash.

## Scope

### In scope
- Store raw file bytes in Redis alongside metadata (no compression — images are already compressed, text files too small to matter)
- Worker downloads bytes from Redis, writes to local temp dir
- Worker appends attachment section to prompt (instead of App)
- Per-file size limit (10 MB) and per-job limit (30 MB)
- Repo cleanup after job completion
- Repo cleanup on graceful shutdown (SIGTERM/SIGINT)
- Stale repo purge on worker startup (crash recovery)

### Out of scope
- S3/MinIO or other object storage (only Redis available)
- Inline file content in prompt (files given as local paths, agent reads them)
- File type processing changes (xlsx parsing, vision — covered by existing `2026-04-09-attachment-support-design.md`)
- Config YAML for size limits (hardcoded initially)

## Architecture

### Attachment data flow

```
BEFORE (broken):
App: Slack → download → /tmp/ → prompt has /tmp paths → Redis {filename, ""}
Worker: Resolve() → {filename, ""} → no-op loop → agent sees dead paths

AFTER:
App: Slack → download → read bytes → Redis {filename, mimetype, raw bytes}
Worker: Resolve() → write to local temp dir → append paths to prompt
```

### Repo isolation: bare cache + worktree

Multiple concurrent jobs may target the same repo. A shared working copy would cause race conditions (one job deletes the repo while another is using it). Solution: bare repo cache + per-job worktrees.

```
/cache/
  bar.git/                ← bare repo (git objects only, no working files)
                             cloned once, then only fetch. Deleted on shutdown/startup.

/tmp/
  triage-repo-<jobA>/     ← worktree for job A (independent working directory)
  triage-repo-<jobB>/     ← worktree for job B (independent working directory)
```

- `git clone --bare` creates the cache (one-time cost)
- `git worktree add` creates per-job working directories (fast, ~hundreds of ms)
- Agent sees a normal repo directory — all files, git commands, skill mounting work identically
- Job completion deletes only its own worktree; other jobs unaffected
- Bare cache deleted only on shutdown / startup purge

### Worker lifecycle

```
Startup:  PurgeStale() → wipe leftover cache dir + stale worktrees
Job done: RemoveWorktree(path) → delete this job's worktree only
Shutdown: CleanAll() → wipe entire cache dir + all worktrees (SIGTERM/SIGINT handler)
Crash:    next startup's PurgeStale() catches it
```

## Data Model Changes

### `queue/job.go`

`AttachmentReady` replaces `URL` with `Data` and `MimeType`:

```go
type AttachmentReady struct {
    Filename string `json:"filename"`
    Data     []byte `json:"data"`      // raw file bytes (base64-encoded in JSON)
    MimeType string `json:"mime_type"` // "image", "text", or "document"
}
```

Note: `URL` field removed — it was never populated, no backward compat concern.

New payload type for `Prepare`:

```go
type AttachmentPayload struct {
    Filename string
    MimeType string
    Data     []byte // raw file bytes (no compression)
    Size     int64  // original size for limit checking
}
```

### `queue/interface.go`

`AttachmentStore.Prepare` signature changes:

```go
type AttachmentStore interface {
    Prepare(ctx context.Context, jobID string, payloads []AttachmentPayload) error
    Resolve(ctx context.Context, jobID string) ([]AttachmentReady, error)
    Cleanup(ctx context.Context, jobID string) error
}
```

### `worker/executor.go`

`RepoProvider` gains worktree and cleanup methods:

```go
type RepoProvider interface {
    Prepare(cloneURL, branch string) (string, error) // returns worktree path (not bare cache path)
    RemoveWorktree(worktreePath string) error        // delete a single job's worktree
    CleanAll() error                                  // delete entire cache dir + all worktrees
    PurgeStale() error                                // startup: wipe leftover state
}
```

## Component Design

### Redis attachment store (`queue/redis_attachments.go`)

**Prepare**: receives `[]AttachmentPayload`, marshals to JSON ([]byte fields become base64 in JSON encoding — adds ~33% size overhead), stores with 30-min TTL. No compression applied (images already compressed, text files too small to benefit). Enforces limits before storing:
- Single file > 10 MB: skip, log warning
- Total job > 30 MB: skip remaining files, log warning
- Note: 30 MB raw → ~40 MB in Redis after base64. Acceptable for expected concurrency.

**Resolve**: unchanged polling pattern. Returns `[]AttachmentReady` now containing `Data` (raw bytes) and `MimeType`.

**Cleanup**: unchanged (`DEL` key).

### In-memory attachment store (`queue/inmem_attachments.go`)

Mirror the same changes for local dev/testing. Channel carries `[]AttachmentReady` with `Data`.

### App side (`bot/workflow.go`)

Changes to `runTriage`:

1. `DownloadAttachments` — unchanged, downloads to temp dir
2. **New**: Read each downloaded file into `[]byte`, build `[]AttachmentPayload`
3. `BuildPrompt` — **remove attachment section** (worker handles this now)
4. `Prepare(ctx, jobID, payloads)` — now sends actual file bytes
5. **Error handling**: if `Prepare` fails (Redis down, OOM, timeout), App must proactively fail the job (update status + publish failed result) so Worker doesn't poll for 3 minutes waiting for data that will never arrive

`defer os.RemoveAll(tempDir)` stays — app cleans its own temp dir after pushing to Redis.

### Prompt (`bot/prompt.go`)

Remove the attachment path section (lines 58-74). Add a new exported function for worker use:

```go
func AppendAttachmentSection(prompt string, attachments []AttachmentInfo) string
```

This generates the same format as before:
```
## Attachments
- /tmp/triage-attach-xxx/screenshot.png (image — use your file reading tools to view)
- /tmp/triage-attach-xxx/error.log (text — read directly)
```

### Worker side (`worker/executor.go`)

Replace the no-op attachment loop with:

1. Create temp dir: `/tmp/triage-attach-<jobID>/`
2. For each `AttachmentReady`: write `Data` to `<temp_dir>/<filename>` (no decompression needed — data is raw bytes)
3. **Filename dedup**: if a filename already exists in temp dir, append suffix (`screenshot.png` → `screenshot_2.png`). Slack threads may have multiple files with the same name from different users.
4. Build `[]AttachmentInfo` from written files
5. Call `AppendAttachmentSection(job.Prompt, attachInfos)` to get final prompt
6. `defer os.RemoveAll(tempDir)` for the attachment temp dir

### Worker pool (`worker/pool.go`)

**Job completion** — in `executeWithTracking`, after publishing result:

```go
// Clean up this job's worktree (not the bare cache).
// repoPath is returned by Prepare() and scoped to this job.
if err := p.cfg.RepoCache.RemoveWorktree(repoPath); err != nil {
    logger.Warn("worktree cleanup failed", "error", err)
}
```

This replaces the current post-kill cleanup that only runs `git checkout .` / `git clean -fd` on failures. Now ALL jobs (success and failure) get worktree removal. The bare cache persists for reuse by future jobs.

**Shutdown** — in `workerHeartbeat`'s `ctx.Done()` branch, after unregistering workers:

```go
if err := p.cfg.RepoCache.CleanAll(); err != nil {
    slog.Warn("shutdown repo cleanup failed", "error", err)
}
```

### RepoCache (`github/repo.go`)

**Core change**: `EnsureRepo` switches from full clone to `git clone --bare`. New `Prepare` adapter creates per-job worktrees via `git worktree add`.

Modified methods:

```go
// EnsureRepo clones a bare repo (or fetches if cached).
// Returns the bare repo path (not a working directory).
func (rc *RepoCache) EnsureRepo(repoRef string) (string, error) {
    // Same logic but uses: git clone --bare <url> <path>
    // Fetch uses: git -C <bare> fetch --all --prune
}
```

New methods:

```go
// AddWorktree creates an isolated working directory from the bare cache.
// Returns the worktree path. Caller must call RemoveWorktree when done.
func (rc *RepoCache) AddWorktree(barePath, branch, worktreePath string) error {
    // git -C <bare> worktree add <worktreePath> origin/<branch>
}

// RemoveWorktree deletes a job's worktree directory.
func (rc *RepoCache) RemoveWorktree(worktreePath string) error {
    // git worktree remove <worktreePath> --force
    // fallback: os.RemoveAll(worktreePath) if git command fails
}

// CleanAll removes the entire cache directory (bare repos + any leftover worktrees).
func (rc *RepoCache) CleanAll() error {
    rc.mu.Lock()
    defer rc.mu.Unlock()
    rc.lastPull = make(map[string]time.Time)
    return os.RemoveAll(rc.dir)
}

// PurgeStale wipes and recreates the cache directory.
// Call on startup to recover from previous unclean shutdown.
func (rc *RepoCache) PurgeStale() error {
    rc.mu.Lock()
    defer rc.mu.Unlock()
    rc.lastPull = make(map[string]time.Time)
    os.RemoveAll(rc.dir)
    return os.MkdirAll(rc.dir, 0755)
}
```

**repoCacheAdapter** (`cmd/agentdock/adapters.go`):

```go
func (a *repoCacheAdapter) Prepare(cloneURL, branch string) (string, error) {
    // 1. EnsureRepo(cloneURL) → bare cache path
    // 2. worktreePath := /tmp/triage-repo-<uuid>/
    // 3. AddWorktree(barePath, branch, worktreePath)
    // 4. return worktreePath
}

func (a *repoCacheAdapter) RemoveWorktree(path string) error {
    return a.cache.RemoveWorktree(path)
}
```

### Worker startup (`cmd/bot/main.go` or worker init)

Call `RepoCache.PurgeStale()` before `Pool.Start()` in worker mode.

## Safety Limits

| Limit | Value | Redis actual | Rationale |
|-------|-------|-------------|-----------|
| Per-file size | 10 MB | ~13.3 MB (base64) | Slack typical max; keeps Redis reasonable |
| Per-job total | 30 MB | ~40 MB (base64) | ~3 files at max; acceptable for expected concurrency |
| Redis TTL | 30 min | — | Existing value; sufficient for job lifecycle |

No compression applied. JSON encoding of `[]byte` fields uses base64, adding ~33% overhead. This is acceptable — complexity of compression logic not worth the savings for files under 10 MB.

Files exceeding limits are silently skipped with a log warning. The job proceeds with whatever files fit — partial attachment is better than no job.

## Cleanup Coverage Matrix

| Scenario | Mechanism | What gets cleaned |
|----------|-----------|-------------------|
| Job completes (success) | `RemoveWorktree(path)` in `executeWithTracking` | Job's worktree dir |
| Job completes (failure) | Same as success (replaces current `git checkout .` hack) | Job's worktree dir |
| Attachment temp files | `defer os.RemoveAll(tempDir)` in `executeJob` | Worker-side attachment dir |
| Redis attachment data | `attachments.Cleanup()` in `ResultListener` (existing) | Redis key |
| Graceful shutdown | `CleanAll()` in `workerHeartbeat` ctx.Done | Entire cache dir (bare repos + worktrees) |
| SIGKILL / OOM / crash | `PurgeStale()` on next startup | Entire cache dir (recreated empty) |
| App-side temp files | `defer os.RemoveAll(tempDir)` in `runTriage` (existing) | App's download dir |
| Prepare fails | App proactively fails job (update status + publish result) | Redis key cleaned by ResultListener |

## Files Changed

| File | Change |
|------|--------|
| `queue/job.go` | Replace `URL` with `Data` + `MimeType` on `AttachmentReady`; add `AttachmentPayload` struct |
| `queue/interface.go` | Change `Prepare` signature to accept `[]AttachmentPayload` |
| `queue/redis_attachments.go` | `Prepare`: store raw bytes; `Resolve`: return bytes; add size limit checks |
| `queue/inmem_attachments.go` | Mirror changes for dev/test |
| `bot/workflow.go` | Read file bytes, build payloads, pass to `Prepare`; fail job on Prepare error; remove attachment paths from prompt building |
| `bot/prompt.go` | Remove attachment section from `BuildPrompt`; add `AppendAttachmentSection` for worker use |
| `worker/executor.go` | Replace no-op loop: write files (with dedup), append to prompt; update `RepoProvider` interface with `RemoveWorktree`/`CleanAll`/`PurgeStale` |
| `worker/pool.go` | Add `RemoveWorktree` after job completion; add `CleanAll` on shutdown |
| `github/repo.go` | Switch `EnsureRepo` to bare clone; add `AddWorktree`, `RemoveWorktree`, `CleanAll`, `PurgeStale` methods |
| `cmd/agentdock/adapters.go` | Update `repoCacheAdapter.Prepare` to use bare+worktree; add `RemoveWorktree`, `CleanAll`, `PurgeStale` |
| `cmd/bot/main.go` | Call `PurgeStale()` on worker startup |

## Testing

- Unit: `redis_attachments_test.go` — Prepare with bytes, Resolve returns bytes, size limit enforcement
- Unit: `inmem_attachments_test.go` — same for in-memory store
- Unit: `prompt_test.go` — `AppendAttachmentSection` output format
- Unit: `repo_test.go` — bare clone, `AddWorktree`, `RemoveWorktree`, `CleanAll`, `PurgeStale` filesystem behavior
- Unit: `executor_test.go` — filename dedup (`screenshot.png` + `screenshot.png` → `screenshot.png` + `screenshot_2.png`)
- Integration: full flow — Prepare with payloads on app side, Resolve + write on worker side, verify file content matches
- Integration: two concurrent jobs same repo — worktrees isolated, one completing doesn't break the other
- Edge: file exceeds 10 MB limit — skipped with warning, job continues
- Edge: job exceeds 30 MB total — partial files stored, job continues
- Edge: Prepare fails after Submit — job is proactively failed, Worker doesn't hang
