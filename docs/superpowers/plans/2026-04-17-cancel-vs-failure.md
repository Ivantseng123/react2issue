# Distinguish User Cancellation from Agent Failure — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the Slack "取消" button produce a clean cancelled state end-to-end (Slack, logs, metrics, GitHub), while keeping true agent failures and admin force-kills routed as `failed`.

**Architecture:** Introduce a `JobCancelled` status. The executor classifies each error site by reading the store — user cancels set `JobCancelled` before sending kill, everything else leaves the store at `JobRunning`/`JobFailed`. A watchdog fallback publishes `cancelled` if the worker crashes mid-cancel. Design A in the result listener (store pre-check) handles the completed-before-cancel race.

**Tech Stack:** Go 1.22+, `log/slog`, `context`, `prometheus/client_golang`. Tests use the standard `testing` package; integration tests under `internal/worker/pool_test.go` use `InMemBundle` helpers from `internal/queue`.

**Spec:** `docs/superpowers/specs/2026-04-17-cancel-vs-failure-design.md`

**Worktree hint:** This plan touches 19 files across queue / worker / bot / config / cmd. Run all tests from repo root with `go test ./...` at the end of each task. Individual test commands are per-task below.

---

## File Structure

Created:
- `internal/worker/executor_test.go` — classifier unit tests
- `internal/bot/agent_test.go` may gain new cases (file already exists; we extend it)

Modified:
- `internal/queue/job.go` — `JobCancelled`, `CancelledAt` field
- `internal/queue/memstore.go` — stamp `CancelledAt`
- `internal/queue/memstore_test.go` — assertions for stamping
- `internal/queue/registry.go` — split `Register` → `RegisterPending` + `SetStarted`
- `internal/queue/registry_test.go` — migrated tests + new assertions
- `internal/queue/watchdog.go` — cancel reorder, `publishCancelledFallback`, back-off, config
- `internal/queue/watchdog_test.go` — fallback + skip + back-off assertions
- `internal/queue/httpstatus.go` — admin guard extension
- `internal/worker/executor.go` — `classifyResult`, `cancelledResult`, pre-`Prepare` guard, call-site migration
- `internal/worker/pool.go` — lifecycle `defer`s, switch short-circuit, race re-read, cancelled log
- `internal/worker/pool_test.go` — cancellation scenarios
- `internal/bot/agent.go` — provider chain early-return on ctx.Canceled
- `internal/bot/result_listener.go` — Design A pre-check, `handleCancellation`, log switch, metrics branches
- `internal/bot/result_listener_test.go` — cancellation + race assertions
- `internal/config/config.go` — `CancelTimeout` in `QueueConfig`, default 60s
- `internal/config/config_test.go` — default + override
- `cmd/agentdock/app.go` — cancel handler order, extended guard, pipe CancelTimeout into `WatchdogConfig`

---

## Task 1: Data Model Changes

**Files:**
- Modify: `internal/queue/job.go`

- [ ] **Step 1: Add `JobCancelled` constant and `CancelledAt` field**

Open `internal/queue/job.go`. In the `JobStatus` const block (around line 10), append:

```go
const (
    JobPending   JobStatus = "pending"
    JobPreparing JobStatus = "preparing"
    JobRunning   JobStatus = "running"
    JobCompleted JobStatus = "completed"
    JobFailed    JobStatus = "failed"
    JobCancelled JobStatus = "cancelled"
)
```

In the `JobState` struct (around line 85), add `CancelledAt`:

```go
type JobState struct {
    Job         *Job
    Status      JobStatus
    Position    int
    WorkerID    string
    StartedAt   time.Time
    WaitTime    time.Duration
    AgentStatus *StatusReport
    CancelledAt time.Time
}
```

- [ ] **Step 2: Verify compilation**

Run: `go build ./...`
Expected: exits 0 (no output).

- [ ] **Step 3: Commit**

```bash
git add internal/queue/job.go
git commit -m "feat(queue): add JobCancelled status and CancelledAt timestamp"
```

---

## Task 2: MemJobStore stamps CancelledAt

**Files:**
- Modify: `internal/queue/memstore.go:58-71`
- Modify: `internal/queue/memstore_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/queue/memstore_test.go`:

```go
func TestMemJobStore_UpdateStatus_StampsCancelledAt(t *testing.T) {
    s := NewMemJobStore()
    s.Put(&Job{ID: "j1"})

    before := time.Now()
    if err := s.UpdateStatus("j1", JobCancelled); err != nil {
        t.Fatalf("UpdateStatus: %v", err)
    }
    state, _ := s.Get("j1")
    if state.CancelledAt.IsZero() {
        t.Fatal("CancelledAt should be stamped")
    }
    if state.CancelledAt.Before(before) {
        t.Errorf("CancelledAt (%v) earlier than call start (%v)", state.CancelledAt, before)
    }
}

func TestMemJobStore_UpdateStatus_CancelledAtIdempotent(t *testing.T) {
    s := NewMemJobStore()
    s.Put(&Job{ID: "j1"})

    s.UpdateStatus("j1", JobCancelled)
    state, _ := s.Get("j1")
    first := state.CancelledAt

    time.Sleep(5 * time.Millisecond)
    s.UpdateStatus("j1", JobCancelled)
    state, _ = s.Get("j1")

    if !state.CancelledAt.Equal(first) {
        t.Errorf("second UpdateStatus should not re-stamp; first=%v second=%v", first, state.CancelledAt)
    }
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/queue/ -run TestMemJobStore_UpdateStatus_Cancelled -v`
Expected: FAIL — `CancelledAt should be stamped` (field is zero).

- [ ] **Step 3: Implement the stamping**

In `internal/queue/memstore.go`, extend `UpdateStatus` (existing code at lines 58-71) to stamp on transition:

```go
func (s *MemJobStore) UpdateStatus(jobID string, status JobStatus) error {
    s.mu.Lock()
    defer s.mu.Unlock()
    state, ok := s.jobs[jobID]
    if !ok {
        return fmt.Errorf("job %q not found", jobID)
    }
    state.Status = status
    if status == JobRunning && state.StartedAt.IsZero() {
        state.StartedAt = time.Now()
        state.WaitTime = state.StartedAt.Sub(state.Job.SubmittedAt)
    }
    if status == JobCancelled && state.CancelledAt.IsZero() {
        state.CancelledAt = time.Now()
    }
    return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/queue/ -run TestMemJobStore_UpdateStatus_Cancelled -v`
Expected: PASS for both tests.

- [ ] **Step 5: Run full queue package**

Run: `go test ./internal/queue/ -v`
Expected: all PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/queue/memstore.go internal/queue/memstore_test.go
git commit -m "feat(queue): stamp CancelledAt when transitioning to JobCancelled"
```

---

## Task 3: ProcessRegistry — split Register

**Files:**
- Modify: `internal/queue/registry.go`
- Modify: `internal/queue/registry_test.go`

Keeps the existing `Register` method alive for now (removed in Task 10 after pool migrates). Adds `RegisterPending` and `SetStarted` plus tests that cover cancel-before-start.

- [ ] **Step 1: Write failing tests**

Append to `internal/queue/registry_test.go`:

```go
func TestProcessRegistry_RegisterPendingThenKill(t *testing.T) {
    reg := NewProcessRegistry()
    ctx, cancel := context.WithCancel(context.Background())

    reg.RegisterPending("j1", cancel)

    go func() {
        <-ctx.Done()
        time.Sleep(10 * time.Millisecond)
        reg.Remove("j1")
    }()

    if err := reg.Kill("j1"); err != nil {
        t.Fatalf("Kill: %v", err)
    }
    if ctx.Err() == nil {
        t.Error("context should be cancelled after Kill on pending entry")
    }
}

func TestProcessRegistry_SetStartedAfterPending(t *testing.T) {
    reg := NewProcessRegistry()
    _, cancel := context.WithCancel(context.Background())

    reg.RegisterPending("j1", cancel)
    reg.SetStarted("j1", 42, "claude")

    agent, ok := reg.Get("j1")
    if !ok {
        t.Fatal("expected agent entry")
    }
    if agent.PID != 42 {
        t.Errorf("PID = %d, want 42", agent.PID)
    }
    if agent.Command != "claude" {
        t.Errorf("Command = %q, want claude", agent.Command)
    }
    if agent.StartedAt.IsZero() {
        t.Error("StartedAt should be set by SetStarted")
    }
}

func TestProcessRegistry_SetStartedWithoutPendingIsNoop(t *testing.T) {
    reg := NewProcessRegistry()
    reg.SetStarted("unknown", 1, "x") // must not panic
    if _, ok := reg.Get("unknown"); ok {
        t.Error("SetStarted without RegisterPending should not create an entry")
    }
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/queue/ -run TestProcessRegistry_ -v`
Expected: FAIL on the new three with `reg.RegisterPending undefined` / `reg.SetStarted undefined`.

- [ ] **Step 3: Add the new methods**

In `internal/queue/registry.go`, after the existing `Register` function (around line 45), add:

```go
func (r *ProcessRegistry) RegisterPending(jobID string, cancel context.CancelFunc) {
    r.mu.Lock()
    defer r.mu.Unlock()
    r.processes[jobID] = &RunningAgent{
        JobID:  jobID,
        cancel: cancel,
        done:   make(chan struct{}),
    }
}

func (r *ProcessRegistry) SetStarted(jobID string, pid int, command string) {
    r.mu.Lock()
    defer r.mu.Unlock()
    if a, ok := r.processes[jobID]; ok {
        a.PID = pid
        a.Command = command
        a.StartedAt = time.Now()
    }
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/queue/ -run TestProcessRegistry_ -v`
Expected: all four PASS (existing + new).

- [ ] **Step 5: Commit**

```bash
git add internal/queue/registry.go internal/queue/registry_test.go
git commit -m "feat(registry): add RegisterPending and SetStarted"
```

---

## Task 4: Executor classifier

**Files:**
- Modify: `internal/worker/executor.go`
- Create: `internal/worker/executor_test.go`

Adds the classifier without migrating existing call sites yet; they stay on `failedResult` until Task 5.

- [ ] **Step 1: Create failing test file**

Create `internal/worker/executor_test.go`:

```go
package worker

import (
    "context"
    "errors"
    "fmt"
    "testing"
    "time"

    "agentdock/internal/queue"
)

func TestClassifyResult_UserCancel(t *testing.T) {
    store := queue.NewMemJobStore()
    store.Put(&queue.Job{ID: "j1"})
    store.UpdateStatus("j1", queue.JobCancelled)

    ctx, cancel := context.WithCancel(context.Background())
    cancel()

    job := &queue.Job{ID: "j1"}
    result := classifyResult(job, time.Now(), fmt.Errorf("killed"), "/tmp/repo", ctx, store)

    if result.Status != "cancelled" {
        t.Errorf("status = %q, want cancelled", result.Status)
    }
    if result.RepoPath != "/tmp/repo" {
        t.Errorf("RepoPath = %q, want /tmp/repo", result.RepoPath)
    }
    if result.Error != "" {
        t.Errorf("Error should be empty for cancelled, got %q", result.Error)
    }
}

func TestClassifyResult_WatchdogKillFallsThroughToFailed(t *testing.T) {
    store := queue.NewMemJobStore()
    store.Put(&queue.Job{ID: "j1"})
    store.UpdateStatus("j1", queue.JobFailed)

    ctx, cancel := context.WithCancel(context.Background())
    cancel()

    result := classifyResult(&queue.Job{ID: "j1"}, time.Now(),
        fmt.Errorf("killed"), "/tmp/repo", ctx, store)

    if result.Status != "failed" {
        t.Errorf("status = %q, want failed", result.Status)
    }
    if result.Error == "" {
        t.Error("Error should be populated for failed")
    }
}

func TestClassifyResult_RunningStoreIsFailed(t *testing.T) {
    store := queue.NewMemJobStore()
    store.Put(&queue.Job{ID: "j1"})
    store.UpdateStatus("j1", queue.JobRunning)

    ctx, cancel := context.WithCancel(context.Background())
    cancel()

    result := classifyResult(&queue.Job{ID: "j1"}, time.Now(),
        errors.New("exit 143"), "/tmp/repo", ctx, store)

    if result.Status != "failed" {
        t.Errorf("status = %q, want failed (store not yet JobCancelled)", result.Status)
    }
}

func TestClassifyResult_DeadlineExceededIsFailed(t *testing.T) {
    store := queue.NewMemJobStore()
    store.Put(&queue.Job{ID: "j1"})
    store.UpdateStatus("j1", queue.JobCancelled) // even with cancelled store…

    ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
    defer cancel()

    result := classifyResult(&queue.Job{ID: "j1"}, time.Now(),
        fmt.Errorf("timeout"), "/tmp/repo", ctx, store)

    if result.Status != "failed" {
        t.Errorf("DeadlineExceeded must yield failed, got %q", result.Status)
    }
}

func TestClassifyResult_NoErrorRoutesToFailed(t *testing.T) {
    store := queue.NewMemJobStore()
    store.Put(&queue.Job{ID: "j1"})

    ctx := context.Background()
    result := classifyResult(&queue.Job{ID: "j1"}, time.Now(),
        errors.New("parse failed"), "/tmp/repo", ctx, store)

    if result.Status != "failed" {
        t.Errorf("status = %q, want failed", result.Status)
    }
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/worker/ -run TestClassifyResult -v`
Expected: FAIL with `undefined: classifyResult`.

- [ ] **Step 3: Implement classifier**

In `internal/worker/executor.go`, add these functions above `failedResult` (around line 191):

```go
func classifyResult(job *queue.Job, startedAt time.Time, err error, repoPath string, ctx context.Context, store queue.JobStore) *queue.JobResult {
    if ctx.Err() == context.Canceled {
        if state, lookupErr := store.Get(job.ID); lookupErr == nil && state.Status == queue.JobCancelled {
            return cancelledResult(job, startedAt, repoPath)
        }
    }
    return failedResult(job, startedAt, err, repoPath)
}

func cancelledResult(job *queue.Job, startedAt time.Time, repoPath string) *queue.JobResult {
    return &queue.JobResult{
        JobID:      job.ID,
        Status:     "cancelled",
        RepoPath:   repoPath,
        StartedAt:  startedAt,
        FinishedAt: time.Now(),
    }
}
```

- [ ] **Step 4: Run to verify PASS**

Run: `go test ./internal/worker/ -run TestClassifyResult -v`
Expected: all five PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/worker/executor.go internal/worker/executor_test.go
git commit -m "feat(worker): add classifyResult to split cancel from failure"
```

---

## Task 5: Migrate executor call sites + pre-Prepare guard

**Files:**
- Modify: `internal/worker/executor.go:41-159`

Switches every failure return in `executeJob` from `failedResult(...)` to `classifyResult(...)` and inserts one ctx check before `Prepare`. `failedResult` remains as a helper used by `classifyResult` internally.

- [ ] **Step 1: Migrate the eight call sites**

Open `internal/worker/executor.go`. Replace each `return failedResult(...)` in `executeJob` with `return classifyResult(..., ctx, deps.store)`. Concretely:

Lines ~52, ~60, ~64, ~68, ~91, ~114, ~128, ~140 — each has shape:

```go
return failedResult(job, startedAt, <errExpr>, <pathExpr>)
```

Change each to:

```go
return classifyResult(job, startedAt, <errExpr>, <pathExpr>, ctx, deps.store)
```

Preserve the original error expressions and repoPath arguments. Do **not** delete `failedResult` — classifier calls it.

- [ ] **Step 2: Add pre-Prepare ctx guard**

Immediately before the `repoPath, err := deps.repoCache.Prepare(...)` call (currently around line 89), add:

```go
if err := ctx.Err(); err != nil {
    return classifyResult(job, startedAt, err, "", ctx, deps.store)
}
```

- [ ] **Step 3: Verify build**

Run: `go build ./...`
Expected: exits 0.

- [ ] **Step 4: Run executor + package tests**

Run: `go test ./internal/worker/ -v`
Expected: all PASS (existing pool tests should still pass because nothing meaningful changed in their setup).

- [ ] **Step 5: Commit**

```bash
git add internal/worker/executor.go
git commit -m "feat(worker): route executor failures through classifier; guard Prepare"
```

---

## Task 6: Agent runner early-return on ctx.Canceled

**Files:**
- Modify: `internal/bot/agent.go:55-70`
- Modify: `internal/bot/agent_test.go`

Stops the provider loop after the first `ctx.Canceled` to eliminate the `所有 agent 已耗盡` spam and change the warn-log verb.

- [ ] **Step 1: Write failing test**

Append to `internal/bot/agent_test.go`:

```go
func TestAgentRunner_CancelShortCircuitsProviderChain(t *testing.T) {
    runner := &AgentRunner{
        agents: []config.AgentConfig{
            {Command: "nonexistent-agent-one", Args: []string{"{prompt}"}, Timeout: time.Second},
            {Command: "nonexistent-agent-two", Args: []string{"{prompt}"}, Timeout: time.Second},
        },
    }
    ctx, cancel := context.WithCancel(context.Background())
    cancel()

    _, err := runner.Run(ctx, slog.Default(), t.TempDir(), "noop", RunOptions{})
    if err == nil {
        t.Fatal("expected error on cancelled ctx")
    }
    if err.Error() != "cancelled" {
        t.Errorf("err = %q, want \"cancelled\" (chain must not try the second agent)", err.Error())
    }
}
```

Why the assertion works:
- Before the fix, both agents are attempted. `exec.CommandContext` fails for each (no binary found), producing `all agents failed: nonexistent-agent-one: ...; nonexistent-agent-two: ...`.
- After the fix, the first agent fails, `ctx.Err() == context.Canceled` matches, and the runner returns `cancelled` without touching the second.

Ensure these imports exist at the top of `agent_test.go` (add any missing):

```go
import (
    "context"
    "log/slog"
    "testing"
    "time"

    "agentdock/internal/config"
)
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/bot/ -run TestAgentRunner_CancelShortCircuits -v`
Expected: FAIL — with current code, iterating two providers produces an error string like `all agents failed: ...`, not `cancelled`.

- [ ] **Step 3: Implement early-return**

Edit `internal/bot/agent.go` `Run` (lines 55-70). Replace the loop body so cancellation breaks out immediately:

```go
func (r *AgentRunner) Run(ctx context.Context, logger *slog.Logger, workDir, prompt string, opts RunOptions) (string, error) {
    var errs []string
    for i, agent := range r.agents {
        logger.Info("嘗試 agent", "phase", "處理中", "command", agent.Command, "index", i, "total", len(r.agents), "timeout", agent.Timeout)
        output, err := r.runOne(ctx, logger, agent, workDir, prompt, opts)
        if err != nil {
            if ctx.Err() == context.Canceled {
                logger.Info("Agent 執行已中斷", "phase", "完成", "command", agent.Command, "index", i)
                return "", fmt.Errorf("cancelled")
            }
            logger.Warn("Agent 失敗", "phase", "失敗", "command", agent.Command, "index", i, "error", err)
            errs = append(errs, fmt.Sprintf("%s: %s", agent.Command, err))
            continue
        }
        logger.Info("Agent 執行成功", "phase", "完成", "command", agent.Command, "output_len", len(output))
        return output, nil
    }
    logger.Error("所有 agent 已耗盡", "phase", "失敗", "errors", strings.Join(errs, "; "))
    return "", fmt.Errorf("all agents failed: %s", strings.Join(errs, "; "))
}
```

- [ ] **Step 4: Run test to verify PASS**

Run: `go test ./internal/bot/ -run TestAgentRunner_CancelShortCircuits -v`
Expected: PASS.

- [ ] **Step 5: Run full bot package**

Run: `go test ./internal/bot/ -v`
Expected: all PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/bot/agent.go internal/bot/agent_test.go
git commit -m "feat(agent): short-circuit provider chain on ctx.Canceled"
```

---

## Task 7: Config — CancelTimeout

**Files:**
- Modify: `internal/config/config.go:65-71,196-204`
- Modify: `internal/config/config_test.go`

Adds `QueueConfig.CancelTimeout` with default 60s.

- [ ] **Step 1: Write failing tests**

The test file uses a `loadFromString(t, yaml)` helper and the pattern from `TestLoad_TrackingTimeoutDefaults` (line 326) and `TestLoad_TrackingTimeouts` (line 305). Mirror those.

Append to `internal/config/config_test.go`:

```go
func TestLoad_CancelTimeoutDefault(t *testing.T) {
    cfg := loadFromString(t, `
agents:
  claude:
    command: claude
`)
    if cfg.Queue.CancelTimeout != 60*time.Second {
        t.Errorf("default cancel_timeout = %v, want 60s", cfg.Queue.CancelTimeout)
    }
}

func TestLoad_CancelTimeoutOverride(t *testing.T) {
    cfg := loadFromString(t, `
queue:
  cancel_timeout: 20s
agents:
  claude:
    command: claude
`)
    if cfg.Queue.CancelTimeout != 20*time.Second {
        t.Errorf("cancel_timeout = %v, want 20s", cfg.Queue.CancelTimeout)
    }
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/config/ -run TestConfig_CancelTimeout -v`
Expected: FAIL (field doesn't exist).

- [ ] **Step 3: Add field and default**

In `internal/config/config.go`, `QueueConfig` (line 65), add:

```go
type QueueConfig struct {
    // ... existing fields ...
    JobTimeout       time.Duration `yaml:"job_timeout"`
    AgentIdleTimeout time.Duration `yaml:"agent_idle_timeout"`
    PrepareTimeout   time.Duration `yaml:"prepare_timeout"`
    CancelTimeout    time.Duration `yaml:"cancel_timeout"`
}
```

In the defaults block (around lines 196-204), append:

```go
if cfg.Queue.CancelTimeout <= 0 {
    cfg.Queue.CancelTimeout = 60 * time.Second
}
```

- [ ] **Step 4: Run to verify PASS**

Run: `go test ./internal/config/ -v`
Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat(config): add queue.cancel_timeout with 60s default"
```

---

## Task 8: Watchdog — cancel reorder, fallback, back-off, config wire

**Files:**
- Modify: `internal/queue/watchdog.go`
- Modify: `internal/queue/watchdog_test.go`

Adds `CancelTimeout` to `WatchdogConfig`, inserts a `JobCancelled` handler ahead of timeout checks, provides `publishCancelledFallback`, and back-offs `killAndPublish` if a cancellation races.

- [ ] **Step 1: Write failing tests**

Append to `internal/queue/watchdog_test.go`:

```go
func TestWatchdog_CancelFallbackAfterTimeout(t *testing.T) {
    store := NewMemJobStore()
    store.Put(&Job{ID: "j1", SubmittedAt: time.Now().Add(-5 * time.Minute)})
    store.UpdateStatus("j1", JobCancelled)
    // Force CancelledAt older than cancelTimeout
    state, _ := store.Get("j1")
    state.CancelledAt = time.Now().Add(-2 * time.Minute)

    results := NewInMemResultBus(10)
    defer results.Close()
    commands := NewInMemCommandBus(10)
    defer commands.Close()

    var onKillReasons []string
    wd := NewWatchdog(store, commands, results, WatchdogConfig{
        JobTimeout:    10 * time.Minute,
        CancelTimeout: 60 * time.Second,
    }, slog.Default(), WithWatchdogKillHook(func(r string) { onKillReasons = append(onKillReasons, r) }))

    wd.check()

    ctx, cancel := context.WithTimeout(context.Background(), time.Second)
    defer cancel()
    ch, _ := results.Subscribe(ctx)

    select {
    case r := <-ch:
        if r.Status != "cancelled" {
            t.Errorf("status = %q, want cancelled", r.Status)
        }
    case <-ctx.Done():
        t.Fatal("no result published")
    }
    if len(onKillReasons) != 1 || onKillReasons[0] != "cancel fallback" {
        t.Errorf("onKill reasons = %v, want [cancel fallback]", onKillReasons)
    }

    // Store status should remain JobCancelled (not flipped to JobFailed)
    state, _ = store.Get("j1")
    if state.Status != JobCancelled {
        t.Errorf("store status = %q, want JobCancelled", state.Status)
    }
}

func TestWatchdog_CancelWithinTimeoutDoesNotFire(t *testing.T) {
    store := NewMemJobStore()
    store.Put(&Job{ID: "j1", SubmittedAt: time.Now().Add(-5 * time.Minute)})
    store.UpdateStatus("j1", JobCancelled) // CancelledAt is now()

    results := NewInMemResultBus(10)
    defer results.Close()
    commands := NewInMemCommandBus(10)
    defer commands.Close()

    wd := NewWatchdog(store, commands, results, WatchdogConfig{
        JobTimeout:    10 * time.Minute,
        CancelTimeout: 60 * time.Second,
    }, slog.Default())

    wd.check()

    ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
    defer cancel()
    ch, _ := results.Subscribe(ctx)
    select {
    case r := <-ch:
        t.Fatalf("unexpected publish: %+v", r)
    case <-ctx.Done():
        // ok
    }
}

func TestWatchdog_CancelStatePreemptsJobTimeout(t *testing.T) {
    store := NewMemJobStore()
    store.Put(&Job{ID: "j1", SubmittedAt: time.Now().Add(-30 * time.Minute)})
    store.UpdateStatus("j1", JobCancelled)

    results := NewInMemResultBus(10)
    defer results.Close()
    commands := NewInMemCommandBus(10)
    defer commands.Close()

    wd := NewWatchdog(store, commands, results, WatchdogConfig{
        JobTimeout:    1 * time.Minute, // already exceeded
        CancelTimeout: 0,               // disable fallback
    }, slog.Default())

    wd.check()

    ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
    defer cancel()
    ch, _ := results.Subscribe(ctx)
    select {
    case r := <-ch:
        t.Fatalf("expected no publish, got %+v", r)
    case <-ctx.Done():
        // ok — cancelled state must not trigger jobTimeout killAndPublish
    }
    state, _ := store.Get("j1")
    if state.Status != JobCancelled {
        t.Errorf("store flipped to %q, should stay JobCancelled", state.Status)
    }
}

func TestWatchdog_KillAndPublishBackOffOnRaceCancel(t *testing.T) {
    store := NewMemJobStore()
    store.Put(&Job{ID: "j1", SubmittedAt: time.Now().Add(-5 * time.Minute)})
    store.UpdateStatus("j1", JobRunning)

    results := NewInMemResultBus(10)
    defer results.Close()
    commands := NewInMemCommandBus(10)
    defer commands.Close()

    wd := NewWatchdog(store, commands, results, WatchdogConfig{}, slog.Default())

    // Simulate race: caller flips to JobCancelled between check() and killAndPublish.
    store.UpdateStatus("j1", JobCancelled)

    state, _ := store.Get("j1")
    wd.killAndPublish(state, "job timeout")

    // Store must remain JobCancelled (back-off kicked in).
    state, _ = store.Get("j1")
    if state.Status != JobCancelled {
        t.Errorf("store flipped to %q, back-off failed", state.Status)
    }
}
```

- [ ] **Step 2: Run to verify failures**

Run: `go test ./internal/queue/ -run TestWatchdog_Cancel -v`
Expected: FAIL (new behaviour not implemented, struct field missing).

- [ ] **Step 3: Extend config and type**

In `internal/queue/watchdog.go`, update `WatchdogConfig`:

```go
type WatchdogConfig struct {
    JobTimeout     time.Duration
    IdleTimeout    time.Duration
    PrepareTimeout time.Duration
    CancelTimeout  time.Duration
}
```

And `Watchdog`:

```go
type Watchdog struct {
    store          JobStore
    commands       CommandBus
    results        ResultBus
    jobTimeout     time.Duration
    idleTimeout    time.Duration
    prepareTimeout time.Duration
    cancelTimeout  time.Duration
    interval       time.Duration
    logger         *slog.Logger
    onKill         func(reason string)
}
```

Update `NewWatchdog` to copy `cfg.CancelTimeout` into `w.cancelTimeout`.

- [ ] **Step 4: Reorder check() and add fallback**

Replace `check()` body (around line 81):

```go
func (w *Watchdog) check() {
    all, err := w.store.ListAll()
    if err != nil {
        w.logger.Warn("Watchdog 列舉工作失敗", "phase", "失敗", "error", err)
        return
    }

    now := time.Now()
    for _, state := range all {
        // Terminal states (no action needed).
        if state.Status == JobCompleted || state.Status == JobFailed {
            continue
        }

        // Cancelled: wait for worker, fall back after cancelTimeout.
        if state.Status == JobCancelled {
            if w.cancelTimeout > 0 && !state.CancelledAt.IsZero() &&
                now.Sub(state.CancelledAt) > w.cancelTimeout {
                w.publishCancelledFallback(state)
            }
            continue
        }

        if now.Sub(state.Job.SubmittedAt) > w.jobTimeout {
            w.killAndPublish(state, "job timeout")
            continue
        }

        if state.Status == JobPreparing && w.prepareTimeout > 0 {
            if state.AgentStatus == nil || state.AgentStatus.LastEventAt.IsZero() {
                if !state.StartedAt.IsZero() && now.Sub(state.StartedAt) > w.prepareTimeout {
                    w.killAndPublish(state, "prepare timeout")
                    continue
                }
            }
        }

        if w.idleTimeout > 0 && state.AgentStatus != nil && !state.AgentStatus.LastEventAt.IsZero() {
            if now.Sub(state.AgentStatus.LastEventAt) > w.idleTimeout {
                w.killAndPublish(state, "agent idle timeout")
                continue
            }
        }
    }
}

func (w *Watchdog) publishCancelledFallback(state *JobState) {
    if w.onKill != nil {
        w.onKill("cancel fallback")
    }
    w.logger.Warn("取消狀態逾時，補發 cancelled result", "phase", "完成",
        "job_id", state.Job.ID,
        "cancelled_age", time.Since(state.CancelledAt))
    if w.results != nil {
        w.results.Publish(context.Background(), &JobResult{
            JobID:      state.Job.ID,
            Status:     "cancelled",
            FinishedAt: time.Now(),
        })
    }
}
```

- [ ] **Step 5: Add killAndPublish back-off**

At the top of `killAndPublish` (before `if w.onKill != nil`), insert:

```go
// Back off if the job was cancelled in the race window.
if fresh, _ := w.store.Get(state.Job.ID); fresh != nil && fresh.Status == JobCancelled {
    return
}
```

- [ ] **Step 6: Run tests to verify PASS**

Run: `go test ./internal/queue/ -run TestWatchdog -v`
Expected: existing plus new four all PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/queue/watchdog.go internal/queue/watchdog_test.go
git commit -m "feat(watchdog): handle JobCancelled with fallback and back-off"
```

---

## Task 9: Admin endpoint guard

**Files:**
- Modify: `internal/queue/httpstatus.go:178-182`

- [ ] **Step 1: Extend the guard**

In `internal/queue/httpstatus.go`, find the admin kill handler's guard (line 178) and add `JobCancelled`:

```go
if state.Status == JobCompleted || state.Status == JobFailed || state.Status == JobCancelled {
    w.WriteHeader(http.StatusConflict)
    json.NewEncoder(w).Encode(map[string]string{"error": "job not running"})
    return
}
```

- [ ] **Step 2: Verify build**

Run: `go build ./...`
Expected: exits 0.

- [ ] **Step 3: Commit**

```bash
git add internal/queue/httpstatus.go
git commit -m "fix(queue): admin force-kill guard includes JobCancelled"
```

---

## Task 10: Pool migration — RegisterPending wiring, switch short-circuit, race re-read, cancelled log

**Files:**
- Modify: `internal/worker/pool.go`
- Modify: `internal/worker/pool_test.go`
- Modify: `internal/queue/registry.go` (removal of old `Register`)
- Modify: `internal/queue/registry_test.go` (drop two test cases that exercised removed `Register`)

This is the largest single change. It migrates pool's registry usage, replaces the short-circuit with a switch covering `JobCancelled` and `JobFailed`, adds the race re-read, changes the final log to distinguish cancelled, and removes the now-unused `Register` method.

- [ ] **Step 1: Write failing pool tests**

Append to `internal/worker/pool_test.go`:

```go
func TestPool_ShortCircuitsCancelledJobAsCancelled(t *testing.T) {
    store := queue.NewMemJobStore()
    bundle := queue.NewInMemBundle(10, 3, store)
    defer bundle.Close()

    job := &queue.Job{ID: "jc", Repo: "o/r", SubmittedAt: time.Now()}
    store.Put(job)
    store.UpdateStatus("jc", queue.JobCancelled)

    pool := NewPool(Config{
        Queue:       bundle.Queue,
        Attachments: bundle.Attachments,
        Results:     bundle.Results,
        Store:       store,
        Runner:      &mockRunner{},
        RepoCache:   &mockRepo{},
        WorkerCount: 1,
        Logger:      slog.Default(),
    })

    ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
    defer cancel()
    pool.Start(ctx)

    if err := bundle.Queue.Submit(ctx, job); err != nil {
        t.Fatalf("Submit: %v", err)
    }

    ch, _ := bundle.Results.Subscribe(ctx)
    select {
    case r := <-ch:
        if r.Status != "cancelled" {
            t.Errorf("status = %q, want cancelled", r.Status)
        }
    case <-ctx.Done():
        t.Fatal("no result")
    }
}

func TestPool_ShortCircuitsFailedJobAsFailed(t *testing.T) {
    store := queue.NewMemJobStore()
    bundle := queue.NewInMemBundle(10, 3, store)
    defer bundle.Close()

    job := &queue.Job{ID: "jf", Repo: "o/r", SubmittedAt: time.Now()}
    store.Put(job)
    store.UpdateStatus("jf", queue.JobFailed)

    pool := NewPool(Config{
        Queue:       bundle.Queue,
        Attachments: bundle.Attachments,
        Results:     bundle.Results,
        Store:       store,
        Runner:      &mockRunner{},
        RepoCache:   &mockRepo{},
        WorkerCount: 1,
        Logger:      slog.Default(),
    })

    ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
    defer cancel()
    pool.Start(ctx)

    if err := bundle.Queue.Submit(ctx, job); err != nil {
        t.Fatalf("Submit: %v", err)
    }

    ch, _ := bundle.Results.Subscribe(ctx)
    select {
    case r := <-ch:
        if r.Status != "failed" {
            t.Errorf("status = %q, want failed", r.Status)
        }
    case <-ctx.Done():
        t.Fatal("no result")
    }
}

type blockingRunner struct {
    started chan struct{}
}

func (b *blockingRunner) Run(ctx context.Context, workDir, prompt string, opts bot.RunOptions) (string, error) {
    if opts.OnStarted != nil {
        opts.OnStarted(1234, "fake")
    }
    close(b.started)
    <-ctx.Done()
    return "", ctx.Err()
}

func TestPool_KillOnRunningAgentProducesCancelledResult(t *testing.T) {
    store := queue.NewMemJobStore()
    bundle := queue.NewInMemBundle(10, 3, store)
    defer bundle.Close()

    job := &queue.Job{ID: "jrun", Repo: "o/r", SubmittedAt: time.Now()}
    store.Put(job)

    runner := &blockingRunner{started: make(chan struct{})}
    pool := NewPool(Config{
        Queue:       bundle.Queue,
        Attachments: bundle.Attachments,
        Results:     bundle.Results,
        Store:       store,
        Runner:      runner,
        RepoCache:   &mockRepo{path: "/tmp/r"},
        Commands:    bundle.Commands,
        WorkerCount: 1,
        Logger:      slog.Default(),
    })

    ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
    defer cancel()
    pool.Start(ctx)

    if err := bundle.Queue.Submit(ctx, job); err != nil {
        t.Fatalf("Submit: %v", err)
    }

    <-runner.started
    // User cancel: mark store then send kill.
    store.UpdateStatus("jrun", queue.JobCancelled)
    bundle.Commands.Send(ctx, queue.Command{JobID: "jrun", Action: "kill"})

    ch, _ := bundle.Results.Subscribe(ctx)
    select {
    case r := <-ch:
        if r.Status != "cancelled" {
            t.Errorf("status = %q, want cancelled", r.Status)
        }
    case <-ctx.Done():
        t.Fatal("no result")
    }
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/worker/ -run TestPool_ShortCircuits -v`
Expected: FAIL — `TestPool_ShortCircuitsFailedJobAsFailed` expects the worker to publish `failed` (currently publishes with the old `JobFailed` branch which would short-circuit as `failed` — this may actually already pass). `TestPool_ShortCircuitsCancelledJobAsCancelled` will FAIL because current pool publishes `failed` for JobFailed only and doesn't know about JobCancelled.

Run: `go test ./internal/worker/ -run TestPool_KillOnRunningAgent -v`
Expected: FAIL — currently returns `failed`.

- [ ] **Step 3: Migrate executeWithTracking registry wiring**

Open `internal/worker/pool.go`. In `executeWithTracking` (line 113), restructure so lifecycle is defer-managed:

```go
func (p *Pool) executeWithTracking(ctx context.Context, workerIndex int, job *queue.Job) {
    logger := p.cfg.Logger.With("worker_id", workerIndex, "job_id", job.ID)
    jobCtx, jobCancel := context.WithCancel(ctx)
    defer jobCancel()

    p.registry.RegisterPending(job.ID, jobCancel)
    defer p.registry.Remove(job.ID)

    // Race mitigation: close the gap between queue-check and RegisterPending
    // for both cancel and admin-failed states.
    if s, _ := p.cfg.Store.Get(job.ID); s != nil &&
        (s.Status == queue.JobCancelled || s.Status == queue.JobFailed) {
        jobCancel()
    }

    wID := fmt.Sprintf("%s/worker-%d", p.cfg.Hostname, workerIndex)

    status := &statusAccumulator{
        jobID:    job.ID,
        workerID: wID,
        alive:    true,
    }

    var stopReporter chan struct{}

    opts := bot.RunOptions{
        OnStarted: func(pid int, command string) {
            status.setPID(pid, command)
            p.registry.SetStarted(job.ID, pid, command)
            logger.Info("Agent 已註冊", "phase", "處理中", "pid", pid, "command", command)

            if p.cfg.Status != nil {
                p.cfg.Status.Report(jobCtx, status.toReport())
                stopReporter = make(chan struct{})
                interval := p.cfg.StatusInterval
                if interval <= 0 {
                    interval = 5 * time.Second
                }
                go p.reportStatus(jobCtx, status, interval, stopReporter)
            }
        },
        OnEvent: func(event queue.StreamEvent) {
            status.recordEvent(event)
        },
    }

    if err := p.cfg.Queue.Ack(jobCtx, job.ID); err != nil {
        logger.Error("ack failed", "error", err)
        p.cfg.Results.Publish(ctx, &queue.JobResult{
            JobID: job.ID, Status: "failed", Error: fmt.Sprintf("ack failed: %v", err),
        })
        if stopReporter != nil {
            close(stopReporter)
        }
        return
    }

    p.cfg.Store.SetWorker(job.ID, wID)

    deps := executionDeps{
        attachments:   p.cfg.Attachments,
        repoCache:     p.cfg.RepoCache,
        runner:        p.cfg.Runner,
        store:         p.cfg.Store,
        skillDirs:     p.cfg.SkillDirs,
        secretKey:     p.cfg.SecretKey,
        workerSecrets: p.cfg.WorkerSecrets,
    }

    result := executeJob(jobCtx, job, deps, opts, logger)
    status.setPrepareSeconds(result.PrepareSeconds)

    status.alive = false
    if p.cfg.Status != nil {
        p.cfg.Status.Report(ctx, status.toReport())
    }

    if stopReporter != nil {
        close(stopReporter)
    }

    if result.RepoPath != "" {
        if err := p.cfg.RepoCache.RemoveWorktree(result.RepoPath); err != nil {
            logger.Warn("Worktree 清理失敗", "phase", "失敗", "path", result.RepoPath, "error", err)
        }
    }

    p.cfg.Store.UpdateStatus(job.ID, queue.JobStatus(result.Status))
    if err := p.cfg.Results.Publish(ctx, result); err != nil {
        logger.Error("failed to publish result", "error", err)
    }

    if result.Status == "cancelled" {
        logger.Info("工作已取消", "phase", "完成")
    } else {
        logger.Info("工作完成", "phase", "完成", "status", result.Status)
    }
}
```

The explicit `p.registry.Remove(job.ID)` at the tail is removed (covered by `defer`).

- [ ] **Step 4: Replace runWorker short-circuit**

In `internal/worker/pool.go runWorker` (around lines 96-104), replace the existing `if err != nil || state.Status == queue.JobFailed { ... continue }` block with:

```go
state, err := p.cfg.Store.Get(job.ID)
if err != nil {
    p.cfg.Results.Publish(ctx, &queue.JobResult{
        JobID: job.ID, Status: "failed", Error: "state lookup failed",
    })
    continue
}
switch state.Status {
case queue.JobCancelled:
    p.cfg.Results.Publish(ctx, &queue.JobResult{
        JobID: job.ID, Status: "cancelled",
    })
    continue
case queue.JobFailed:
    p.cfg.Results.Publish(ctx, &queue.JobResult{
        JobID: job.ID, Status: "failed", Error: "cancelled before execution",
    })
    continue
}
p.executeWithTracking(ctx, id, job)
```

- [ ] **Step 5: Remove the old `Register` method**

Open `internal/queue/registry.go`. Delete the existing `Register(jobID string, pid int, command string, cancel context.CancelFunc)` function.

Open `internal/queue/registry_test.go`. Delete `TestProcessRegistry_RegisterAndKill` and `TestProcessRegistry_RemoveClosesDone` (they exercised the removed API). Keep `TestProcessRegistry_KillNotFound`, `TestProcessRegistry_RegisterPendingThenKill`, `TestProcessRegistry_SetStartedAfterPending`, `TestProcessRegistry_SetStartedWithoutPendingIsNoop`.

- [ ] **Step 6: Verify build**

Run: `go build ./...`
Expected: exits 0. No remaining `registry.Register` callers.

- [ ] **Step 7: Run worker + queue tests**

Run: `go test ./internal/worker/ ./internal/queue/ -v`
Expected: all PASS including the three new pool tests.

- [ ] **Step 8: Commit**

```bash
git add internal/worker/pool.go internal/worker/pool_test.go internal/queue/registry.go internal/queue/registry_test.go
git commit -m "feat(worker): switch short-circuit, RegisterPending wiring, cancelled log"
```

---

## Task 11: Result Listener — Design A, handleCancellation, log switch, metrics

**Files:**
- Modify: `internal/bot/result_listener.go`
- Modify: `internal/bot/result_listener_test.go`

- [ ] **Step 1: Write failing tests**

Append to `internal/bot/result_listener_test.go`:

```go
func TestResultListener_CancelledResultUpdatesSlack(t *testing.T) {
    store := queue.NewMemJobStore()
    store.Put(&queue.Job{ID: "jcan", Repo: "o/r", ChannelID: "C1", ThreadTS: "T1", StatusMsgTS: "S1"})

    bundle := queue.NewInMemBundle(10, 3, store)
    defer bundle.Close()

    slackMock := &mockSlackPoster{}
    listener := NewResultListener(bundle.Results, store, bundle.Attachments, slackMock, nil, nil, slog.Default())

    ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
    defer cancel()
    go listener.Listen(ctx)

    bundle.Results.Publish(ctx, &queue.JobResult{JobID: "jcan", Status: "cancelled"})
    time.Sleep(200 * time.Millisecond)

    slackMock.mu.Lock()
    defer slackMock.mu.Unlock()
    found := false
    for _, msg := range slackMock.messages {
        if strings.Contains(msg, "已取消") {
            found = true
        }
    }
    if !found {
        t.Errorf("expected cancelled message, got %v", slackMock.messages)
    }
    if len(slackMock.buttons) != 0 {
        t.Errorf("no retry button expected, got %v", slackMock.buttons)
    }

    state, _ := store.Get("jcan")
    if state.Status != queue.JobCancelled {
        t.Errorf("store status = %q, want JobCancelled", state.Status)
    }
}

func TestResultListener_CompletedResultDeferredToCancellationWhenStoreCancelled(t *testing.T) {
    store := queue.NewMemJobStore()
    store.Put(&queue.Job{ID: "jrace", Repo: "o/r", ChannelID: "C1", ThreadTS: "T1", StatusMsgTS: "S1"})
    store.UpdateStatus("jrace", queue.JobCancelled)

    bundle := queue.NewInMemBundle(10, 3, store)
    defer bundle.Close()

    slackMock := &mockSlackPoster{}
    github := &mockIssueCreator{url: "https://github.com/o/r/issues/42"}
    listener := NewResultListener(bundle.Results, store, bundle.Attachments, slackMock, github, nil, slog.Default())

    ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
    defer cancel()
    go listener.Listen(ctx)

    bundle.Results.Publish(ctx, &queue.JobResult{
        JobID: "jrace", Status: "completed",
        Title: "Bug", Body: "b", Confidence: "high", FilesFound: 2,
    })
    time.Sleep(200 * time.Millisecond)

    slackMock.mu.Lock()
    defer slackMock.mu.Unlock()

    for _, msg := range slackMock.messages {
        if strings.Contains(msg, "issues/42") {
            t.Errorf("issue URL must not be posted when store says cancelled; messages=%v", slackMock.messages)
        }
    }
    found := false
    for _, msg := range slackMock.messages {
        if strings.Contains(msg, "已取消") {
            found = true
        }
    }
    if !found {
        t.Errorf("expected cancelled message, got %v", slackMock.messages)
    }
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/bot/ -run TestResultListener_Cancelled -v`
Run: `go test ./internal/bot/ -run TestResultListener_CompletedResultDeferredToCancellation -v`
Expected: FAIL on both.

- [ ] **Step 3: Add `handleCancellation` and pre-check**

Open `internal/bot/result_listener.go`. Edit `handleResult` (starts line 82). After `r.recordMetrics(...)` and before the `switch` block (currently around line 115), add the store pre-check:

```go
// Design A: user cancellation dominates, regardless of result.Status.
if state.Status == queue.JobCancelled || result.Status == "cancelled" {
    r.handleCancellation(state.Job, state, result)
    r.attachments.Cleanup(ctx, result.JobID)
    return
}
```

Then adjust the `switch` so it no longer handles `"cancelled"` (the pre-check took that branch) — leave `failed`, `low confidence`, degraded, success as-is.

Also replace the top-level log block (lines 104-113) with a switch:

```go
logger := r.logger.With("job_id", result.JobID, "repo", job.Repo, "status", result.Status)
switch result.Status {
case "failed":
    truncated := result.RawOutput
    if len(truncated) > 2000 {
        truncated = truncated[:2000] + "…(truncated)"
    }
    logger.Warn("工作失敗", "phase", "降級", "error", result.Error, "raw_output", truncated)
case "cancelled":
    logger.Info("工作已取消", "phase", "完成")
default:
    logger.Info("工作完成", "phase", "完成", "title", result.Title, "confidence", result.Confidence, "files_found", result.FilesFound)
}
```

After the existing `handleFailure` / `createAndPostIssue` / etc methods, add:

```go
func (r *ResultListener) handleCancellation(job *queue.Job, state *queue.JobState, result *queue.JobResult) {
    r.store.UpdateStatus(job.ID, queue.JobCancelled)
    r.updateStatus(job, ":white_check_mark: 已取消")
    r.clearDedup(job)
}
```

- [ ] **Step 4: Extend metrics branches**

In `recordMetrics`, update the with-AgentStatus branch's status derivation:

```go
status := "success"
switch result.Status {
case "failed":
    if strings.Contains(result.Error, "timeout") {
        status = "timeout"
    } else {
        status = "error"
    }
case "cancelled":
    status = "cancelled"
}
metrics.AgentExecutionsTotal.WithLabelValues(provider, status).Inc()
```

And the without-AgentStatus else-if chain:

```go
} else if result.Status == "failed" {
    metrics.AgentExecutionsTotal.WithLabelValues("unknown", "error").Inc()
} else if result.Status == "cancelled" {
    metrics.AgentExecutionsTotal.WithLabelValues("unknown", "cancelled").Inc()
}
```

- [ ] **Step 5: Run tests to verify PASS**

Run: `go test ./internal/bot/ -v`
Expected: all PASS including the two new cancellation tests.

- [ ] **Step 6: Commit**

```bash
git add internal/bot/result_listener.go internal/bot/result_listener_test.go
git commit -m "feat(bot): handle cancelled results with store-first semantics"
```

---

## Task 12: App cancel handler — order fix, extended guard, wire CancelTimeout

**Files:**
- Modify: `cmd/agentdock/app.go`

- [ ] **Step 1: Update the cancel_job handler**

Find the `case strings.HasPrefix(action.ActionID, "cancel_job"):` block (around line 373). Replace with:

```go
case strings.HasPrefix(action.ActionID, "cancel_job"):
    jobID := action.Value
    state, err := jobStore.Get(jobID)
    if err == nil &&
        state.Status != queue.JobFailed &&
        state.Status != queue.JobCompleted &&
        state.Status != queue.JobCancelled {
        // Order matters: UpdateStatus BEFORE Send so the worker's classifyResult
        // observes JobCancelled when ctx cancellation propagates.
        jobStore.UpdateStatus(jobID, queue.JobCancelled)
        bundle.Commands.Send(context.Background(), queue.Command{JobID: jobID, Action: "kill"})
        slackClient.UpdateMessage(cb.Channel.ID, selectorTS, ":stop_sign: 正在取消...")
        handler.ClearThreadDedup(cb.Channel.ID, state.Job.ThreadTS)
    } else {
        slackClient.UpdateMessage(cb.Channel.ID, selectorTS, ":information_source: 此任務已結束")
    }
```

- [ ] **Step 2: Pass CancelTimeout into WatchdogConfig**

Find where `NewWatchdog(...)` is called in `cmd/agentdock/app.go`. Look for the `WatchdogConfig{...}` literal and add the new field:

```go
WatchdogConfig{
    JobTimeout:     cfg.Queue.JobTimeout,
    IdleTimeout:    cfg.Queue.AgentIdleTimeout,
    PrepareTimeout: cfg.Queue.PrepareTimeout,
    CancelTimeout:  cfg.Queue.CancelTimeout,
}
```

If the exact `WatchdogConfig` construction is in a separate file or helper, edit there. Use `grep -rn "WatchdogConfig{" cmd internal` to locate.

- [ ] **Step 3: Verify build and full test suite**

Run: `go build ./...`
Expected: exits 0.

Run: `go test ./...`
Expected: all PASS.

- [ ] **Step 4: Commit**

```bash
git add cmd/agentdock/app.go
git commit -m "fix(app): order UpdateStatus before kill; wire cancel_timeout; guard cancelled"
```

---

## Final Verification

- [ ] **Step 1: Full suite**

Run: `go test ./...`
Expected: all PASS.

- [ ] **Step 2: Race detector**

Run: `go test -race ./internal/worker/ ./internal/queue/ ./internal/bot/`
Expected: no data races reported.

- [ ] **Step 3: Manual smoke**

Instructions for the operator (not automatable):

1. `make dev` (or equivalent) to run app + worker locally.
2. Trigger a triage in a test Slack channel (`@bot` in a thread).
3. While the agent is running, click the 取消 button.
4. Verify:
   - Slack updates to `:stop_sign: 正在取消...`.
   - Within a few seconds, Slack updates to `:white_check_mark: 已取消`.
   - No retry button.
   - Worker log line `[Worker][完成] 工作已取消` appears.
5. Re-tag the bot in the same thread; triage should work (dedup cleared).
6. Repeat with the click happening during repo clone (submit a large repo with cold cache). Clone completes, then Slack still ends at `:white_check_mark: 已取消`.
7. Repeat with a cancelled job that never got to a worker (cancel immediately after submission): the queue-pulling worker should publish `cancelled` via the short-circuit; Slack ends at `:white_check_mark: 已取消`.
