# PR Review SHA Ref + Worker Fetch-Retry Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix the "workdir prepare failed" failure mode that triggered after PR #130 merged. Make worker resilient to unknown git refs (deleted branches or unreachable SHAs), make worker log the underlying git error on Prepare failure, and switch PR Review to reference the head commit by immutable SHA.

**Architecture:** Three independent changes in three Go modules — order matters because each task strictly improves on the previous failure mode. Task 1 hardens `shared/github/RepoCache.AddWorktree` with on-demand fetch retry (helps every workflow). Task 2 adds worker-side error logging on Prepare failure. Task 3 makes PR Review carry `pr.Head.SHA` in `Job.Branch` while keeping the human-readable branch name in `PromptContext.Branch`.

**Tech Stack:** Go 1.25, three modules (`shared/`, `app/`, `worker/`), git CLI, real-git integration tests via `file://` remotes. Test framework: stdlib `testing`. No new dependencies.

**Spec:** `docs/superpowers/specs/2026-04-23-pr-review-sha-ref-design.md`

---

## File Structure

| Path | Module | Responsibility |
|---|---|---|
| `shared/github/repo.go` | shared | `RepoCache.AddWorktree` extended with fetch-retry; new private `tryAddWorktree` helper |
| `shared/github/repo_test.go` | shared | Two new integration-style unit tests using `file://` remote with `uploadpack.allowAnySHA1InWant=true` |
| `worker/pool/executor.go` | worker | One new `logger.Error` line on Prepare failure |
| `app/workflow/pr_review.go` | app | `prReviewState.HeadSHA` added; `validateAndBuild` populates it; `BuildJob` uses it for `Job.Branch`, keeps `HeadRef` for `PromptContext.Branch` |
| `app/workflow/pr_review_test.go` | app | Two new unit tests covering the SHA propagation |

The three modules each commit independently. Order: shared → worker → app, so the safety net (fetch retry) lands before the change that depends on it (SHA-based ref).

---

## Task 1: Fetch-retry in `RepoCache.AddWorktree`

**Files:**
- Modify: `shared/github/repo.go:196-209`
- Test: `shared/github/repo_test.go` (append two new tests at end of `TestRepoCache_AddWorktree_*` family, before the `run` helper at line 352)

**Background for the implementer:**

Today `RepoCache.AddWorktree` runs `git worktree add --detach <wt> <ref>` once. If `<ref>` is a branch name that no longer exists locally (e.g. PR merged + branch deleted on GitHub + `git fetch --prune` removed the local tracking ref) or a commit SHA whose object was never fetched (e.g. squash-merged PR head), git returns `invalid reference` and prepare fails.

GitHub enables `uploadpack.allowReachableSHA1InWant`, so `git fetch origin <SHA>` works even when no local ref points to the SHA. A `git fetch origin <branch>` works for any still-existing branch. Either way, after the fetch succeeds the object is in the local object DB and `git worktree add --detach <SHA>` works.

The retry pattern is: try once → on failure, fetch the ref by name from origin → retry once more. We do not parse stderr to decide whether to fetch — every first-attempt failure gets the fallback. The cost of a redundant fetch on a different failure (worktree path collision, etc.) is one round trip; the failure replays identically and the user sees the same wrapped error.

The fetch uses `origin` directly. `EnsureRepo` configures `origin` with the appropriate token via `resolveURLWithToken` (`shared/github/repo.go:45-64`), so `AddWorktree`'s signature stays unchanged.

- [ ] **Step 1: Write the failing positive test**

Append to `shared/github/repo_test.go`, after `TestRepoCache_AddWorktree_PrunesStaleAdminRecord` (line 235) and before `TestRepoCache_CleanAll` (line 237):

```go
func TestRepoCache_AddWorktree_RetriesAfterFetchOnUnknownRef(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}

	// Source: bare repo with a "feature" branch + main. The feature commit's
	// SHA is what we'll later try to worktree-add after the branch is gone.
	sourceDir := t.TempDir()
	run(t, sourceDir, "git", "init", "--bare")
	// Allow fetch-by-SHA against this remote; mirrors GitHub's
	// uploadpack.allowReachableSHA1InWant behaviour.
	run(t, sourceDir, "git", "config", "uploadpack.allowAnySHA1InWant", "true")

	workDir := t.TempDir()
	run(t, workDir, "git", "clone", sourceDir, ".")
	os.WriteFile(filepath.Join(workDir, "main.go"), []byte("package main"), 0644)
	run(t, workDir, "git", "add", ".")
	run(t, workDir, "git", "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-m", "init")
	run(t, workDir, "git", "push")
	run(t, workDir, "git", "checkout", "-b", "feature")
	os.WriteFile(filepath.Join(workDir, "feature.go"), []byte("package main"), 0644)
	run(t, workDir, "git", "add", ".")
	run(t, workDir, "git", "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-m", "add feature")
	run(t, workDir, "git", "push", "-u", "origin", "feature")

	// Capture feature SHA, then delete the feature branch from source. The
	// commit object stays reachable on the source server because Git keeps
	// objects until gc; allowAnySHA1InWant lets clients fetch them.
	shaOut, err := exec.Command("git", "-C", workDir, "rev-parse", "feature").Output()
	if err != nil {
		t.Fatalf("rev-parse feature: %v", err)
	}
	featureSHA := strings.TrimSpace(string(shaOut))
	run(t, sourceDir, "git", "update-ref", "-d", "refs/heads/feature")

	// Fresh cache: EnsureRepo only pulls refs reachable from refs/heads/*,
	// so the deleted feature branch's commit is NOT in the cache.
	cacheDir := t.TempDir()
	cache := NewRepoCache(cacheDir, time.Hour, "", slog.Default())
	barePath, err := cache.EnsureRepo("file://"+sourceDir, "")
	if err != nil {
		t.Fatalf("EnsureRepo failed: %v", err)
	}

	// Sanity-check: SHA is unreachable locally before AddWorktree.
	if out, err := exec.Command("git", "-C", barePath, "cat-file", "-t", featureSHA).CombinedOutput(); err == nil {
		t.Fatalf("test setup invariant broken: feature SHA %s already reachable in cache (%s)", featureSHA, out)
	}

	// AddWorktree by SHA should fetch-retry and succeed.
	wt := filepath.Join(t.TempDir(), "wt")
	if err := cache.AddWorktree(barePath, featureSHA, wt); err != nil {
		t.Fatalf("AddWorktree(%s) failed: %v", featureSHA, err)
	}

	// Worktree HEAD should be the feature SHA.
	headOut, err := exec.Command("git", "-C", wt, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("rev-parse HEAD in worktree: %v", err)
	}
	if got := strings.TrimSpace(string(headOut)); got != featureSHA {
		t.Errorf("worktree HEAD = %s, want %s", got, featureSHA)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```
cd shared && go test ./github -run TestRepoCache_AddWorktree_RetriesAfterFetchOnUnknownRef -v
```

Expected: FAIL. Error message will contain `git worktree add: ... invalid reference: <SHA>` because the current `AddWorktree` does no retry.

- [ ] **Step 3: Write the failing negative test**

Immediately after the test from Step 1, append:

```go
func TestRepoCache_AddWorktree_PropagatesErrorWhenFetchAlsoFails(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}

	// Source: bare repo with main only. Set allowAnySHA1InWant so the fetch
	// attempt is well-formed; failure must come from the SHA not existing
	// on the server, not from server-side policy.
	sourceDir := t.TempDir()
	run(t, sourceDir, "git", "init", "--bare")
	run(t, sourceDir, "git", "config", "uploadpack.allowAnySHA1InWant", "true")

	workDir := t.TempDir()
	run(t, workDir, "git", "clone", sourceDir, ".")
	os.WriteFile(filepath.Join(workDir, "main.go"), []byte("package main"), 0644)
	run(t, workDir, "git", "add", ".")
	run(t, workDir, "git", "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-m", "init")
	run(t, workDir, "git", "push")

	cacheDir := t.TempDir()
	cache := NewRepoCache(cacheDir, time.Hour, "", slog.Default())
	barePath, err := cache.EnsureRepo("file://"+sourceDir, "")
	if err != nil {
		t.Fatalf("EnsureRepo failed: %v", err)
	}

	bogusSHA := "deadbeef00000000000000000000000000000beef"
	wt := filepath.Join(t.TempDir(), "wt")
	err = cache.AddWorktree(barePath, bogusSHA, wt)
	if err == nil {
		t.Fatal("expected error when ref is unknown to both cache and remote, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "worktree add") {
		t.Errorf("error should mention worktree add: %q", msg)
	}
	if !strings.Contains(msg, "fetch") {
		t.Errorf("error should mention fetch fallback failure: %q", msg)
	}
}
```

- [ ] **Step 4: Run both tests to verify they fail**

```
cd shared && go test ./github -run TestRepoCache_AddWorktree -v
```

Expected: the two new tests FAIL (positive: no retry; negative: error string lacks `fetch`). Existing `TestRepoCache_AddWorktree_*` tests still PASS.

- [ ] **Step 5: Implement fetch-retry in `AddWorktree`**

In `shared/github/repo.go`, replace the existing `AddWorktree` (lines 192-209) with:

```go
// AddWorktree creates a detached-HEAD worktree from a bare cache. --detach
// avoids locking the branch so concurrent jobs on the same branch coexist and
// a prior crash's orphan admin record can't block new adds. Prunes first to
// clear any orphan <bare>/worktrees/NAME records left by past crashes.
//
// If the first add fails (typically: ref unknown to the local cache because
// the branch was deleted on origin or the SHA was never fetched — e.g. PR
// merged + branch deleted, or squash-merged PR head SHA), AddWorktree fetches
// the ref directly from origin and retries once. GitHub enables
// uploadpack.allowReachableSHA1InWant, so a direct fetch-by-SHA works even
// when no remote ref still points at the SHA.
func (rc *RepoCache) AddWorktree(barePath, branch, worktreePath string) error {
	pruneCmd := exec.Command("git", "-C", barePath, "worktree", "prune")
	_, _ = pruneCmd.CombinedOutput() // best-effort

	ref := "HEAD"
	if branch != "" {
		ref = branch
	}

	addErr := tryAddWorktree(barePath, worktreePath, ref)
	if addErr == nil {
		return nil
	}

	fetchCmd := exec.Command("git", "-C", barePath, "fetch", "origin", ref)
	fetchOut, fetchErr := fetchCmd.CombinedOutput()
	if fetchErr != nil {
		return fmt.Errorf("git worktree add failed: %w; git fetch origin %s also failed: %v\n%s",
			addErr, ref, fetchErr, fetchOut)
	}

	if retryErr := tryAddWorktree(barePath, worktreePath, ref); retryErr != nil {
		return fmt.Errorf("git worktree add failed even after fetch retry: %w", retryErr)
	}
	return nil
}

// tryAddWorktree runs `git worktree add --detach` once and returns nil on
// success or a wrapped error containing git's stderr on failure. Extracted
// from AddWorktree so the retry path can call it without duplicating the
// command construction.
func tryAddWorktree(barePath, worktreePath, ref string) error {
	cmd := exec.Command("git", "-C", barePath, "worktree", "add", "--detach", worktreePath, ref)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%w\n%s", err, out)
	}
	return nil
}
```

- [ ] **Step 6: Run all `repo_test.go` tests to verify pass + no regression**

```
cd shared && go test ./github -v
```

Expected: ALL tests PASS, including the two new ones from Steps 1 and 3, and all existing `TestRepoCache_*` tests.

- [ ] **Step 7: Commit**

```
git add shared/github/repo.go shared/github/repo_test.go
git commit -m "$(cat <<'EOF'
fix(github): RepoCache.AddWorktree fetches missing refs on demand

When a worker tries to add a worktree for a branch that has been deleted
on origin (e.g. after PR merge + branch deletion) or a SHA whose object
was never fetched (e.g. squash-merged PR head), git fails with "invalid
reference" and prepare fails for the whole job.

Add a one-shot retry: on first failure, run "git fetch origin <ref>"
and retry "git worktree add --detach". Works for both branch names and
commit SHAs because GitHub enables uploadpack.allowReachableSHA1InWant.

Closes the immediate cause of the 2026-04-23 PR Review failure on PR
#130 (head branch deleted post-merge before worker picked up the job).

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 2: Worker logs Prepare failure detail

**Files:**
- Modify: `worker/pool/executor.go:95-98`

**Background for the implementer:**

Today when `provider.Prepare` returns an error, `executor.go` wraps it as `workdir prepare failed: %w` and returns a failed `JobResult` to the app. The error string contains git's stderr (because `RepoCache.AddWorktree` includes it in the wrap) and the app surfaces it to Slack as `:x: 分析失敗: workdir prepare failed: ...`. But the worker pod's stdout never logs the wrapped error — operators looking at `kubectl logs` see only `工作完成 ... status=failed` and have no diagnostic breadcrumb without re-running.

Add one `logger.Error` call before the existing `classifyResult`. Mirror the structured `branch` field from the existing prepare-start `Info` log at line 85 so operators can `grep "branch=<X>"` to find all failures involving a given ref.

No automated test — capturing `slog` output for a single literal call is brittle and the cost outweighs the value (per spec Out-of-Scope).

- [ ] **Step 1: Add the error log line**

In `worker/pool/executor.go`, around line 94-98, change:

```go
provider := selectProvider(job, deps.repoCache, ghToken)
repoPath, err := provider.Prepare(job)
if err != nil {
	return classifyResult(job, startedAt, fmt.Errorf("workdir prepare failed: %w", err), "", ctx, deps.store)
}
```

to:

```go
provider := selectProvider(job, deps.repoCache, ghToken)
repoPath, err := provider.Prepare(job)
if err != nil {
	logger.Error("Repo 準備失敗", "phase", "失敗", "branch", job.Branch, "error", err.Error())
	return classifyResult(job, startedAt, fmt.Errorf("workdir prepare failed: %w", err), "", ctx, deps.store)
}
```

- [ ] **Step 2: Run worker tests to verify no regression**

```
cd worker && go test ./pool -v
```

Expected: existing tests PASS unchanged; no new tests added.

- [ ] **Step 3: Commit**

```
git add worker/pool/executor.go
git commit -m "$(cat <<'EOF'
feat(worker): log Repo prepare failure with git stderr at error level

executor.go wraps Prepare errors and returns them to the app, but the
worker pod never logged the wrapped error. Operators looking at
kubectl logs only saw "工作完成 ... status=failed" with no breadcrumb.

Add a logger.Error call before classifyResult so the underlying git
stderr (carried inside err.Error()) lands in the worker's stdout log.
Structured "branch" field mirrors the prepare-start Info log so ops
can grep "branch=<X>" to trace failures by ref.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 3: PR Review uses head SHA for `Job.Branch`

**Files:**
- Modify: `app/workflow/pr_review.go:35-43` (struct), `:155-163` (validateAndBuild), `:252-273` (BuildJob)
- Test: `app/workflow/pr_review_test.go` (append two new tests after `TestPRReviewWorkflow_HandleResult_ErrorStatus` at line 201, before `newTestPRReviewWorkflow` helper at line 203)

**Background for the implementer:**

Today `BuildJob` passes `pr.Head.Ref` (a branch name like `feat/prompt-response-schema`) into `Job.Branch`. Worker passes that to `git worktree add --detach`. When the PR is merged and the branch is deleted on GitHub, the worker can't find the ref. Task 1 made the worker resilient to this via fetch-retry, but the cleanest fix at the source is to send the immutable head SHA — `pr.Head.SHA` is already in the API response (`shared/github/pr_types.go:19`).

`Job.Branch` and `PromptContext.Branch` look like the same thing but serve different consumers. `Job.Branch` is the **git ref** consumed by `git worktree add` in `shared/github/repo.go`. `PromptContext.Branch` is **prose** rendered into the agent's prompt by `worker/prompt`. The git layer needs identifier stability (SHA); the prompt layer needs human readability (branch name). Splitting these in `BuildJob` localises the change to one workflow file.

- [ ] **Step 1: Write the failing `BuildJob` test**

Append to `app/workflow/pr_review_test.go`, after `TestPRReviewWorkflow_HandleResult_ErrorStatus` (line 201) and before `newTestPRReviewWorkflow` (line 203):

```go
func TestPRReviewWorkflow_BuildJob_UsesHeadSHAAsJobBranch(t *testing.T) {
	w, _ := newTestPRReviewWorkflow(t)
	pending := &Pending{
		ChannelID:   "C1",
		ThreadTS:    "1.0",
		Reporter:    "reporter",
		ChannelName: "general",
		RequestID:   "req-1",
		TaskType:    "pr_review",
		State: &prReviewState{
			URL:      "https://github.com/foo/bar/pull/7",
			Owner:    "foo",
			Repo:     "bar",
			Number:   7,
			HeadRepo: "forker/bar",
			HeadRef:  "feat/x",
			HeadSHA:  "abc1234567890def1234567890abc1234567890d",
			BaseRef:  "main",
		},
	}
	job, _, err := w.BuildJob(context.Background(), pending)
	if err != nil {
		t.Fatalf("BuildJob failed: %v", err)
	}
	if job.Branch != "abc1234567890def1234567890abc1234567890d" {
		t.Errorf("Job.Branch = %q, want head SHA", job.Branch)
	}
	if job.PromptContext == nil || job.PromptContext.Branch != "feat/x" {
		t.Errorf("PromptContext.Branch = %q, want HeadRef \"feat/x\"",
			job.PromptContext.Branch)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```
cd app && go test ./workflow -run TestPRReviewWorkflow_BuildJob_UsesHeadSHAAsJobBranch -v
```

Expected: COMPILE FAIL — `prReviewState` has no `HeadSHA` field yet. (This is the test failure mode for "field doesn't exist"; once we add the field in Step 5 the compile passes and the assertion runs.)

- [ ] **Step 3: Write the failing `validateAndBuild` test**

Immediately after the test from Step 1, append:

```go
func TestPRReviewWorkflow_ValidateAndBuild_PopulatesHeadSHA(t *testing.T) {
	pr := &ghclient.PullRequest{Number: 7, State: "open", Title: "T"}
	pr.Head.Ref = "feat/x"
	pr.Head.SHA = "deadbeef0000000000000000000000000000beef"
	pr.Head.Repo.FullName = "forker/bar"
	pr.Head.Repo.CloneURL = "https://github.com/forker/bar.git"
	pr.Base.Ref = "main"

	w, _ := newTestPRReviewWorkflow(t)
	w.github = &fakeGitHubPR{pr: pr}

	step, err := w.Trigger(context.Background(), TriggerEvent{ChannelID: "C1", ThreadTS: "1.0"}, "https://github.com/foo/bar/pull/7")
	if err != nil {
		t.Fatal(err)
	}
	if step.Kind != NextStepSubmit {
		t.Fatalf("expected NextStepSubmit, got %v", step.Kind)
	}
	st, ok := step.Pending.State.(*prReviewState)
	if !ok {
		t.Fatalf("State is not *prReviewState: %T", step.Pending.State)
	}
	if st.HeadSHA != "deadbeef0000000000000000000000000000beef" {
		t.Errorf("HeadSHA = %q, want \"deadbeef0000000000000000000000000000beef\"", st.HeadSHA)
	}
}
```

- [ ] **Step 4: Run both new tests to verify they fail**

```
cd app && go test ./workflow -run "TestPRReviewWorkflow_BuildJob_UsesHeadSHAAsJobBranch|TestPRReviewWorkflow_ValidateAndBuild_PopulatesHeadSHA" -v
```

Expected: COMPILE FAIL — `prReviewState.HeadSHA` doesn't exist. Existing `TestPRReviewWorkflow_TriggerAPath_Valid` already references `pr.Head.SHA` in its setup but doesn't assert on `HeadSHA` flowing into state, so it stays passing once the field is added.

- [ ] **Step 5: Add `HeadSHA` field, populate it, use it in `BuildJob`**

In `app/workflow/pr_review.go`, find the `prReviewState` struct (around line 35) and add `HeadSHA`:

```go
type prReviewState struct {
	URL      string
	Owner    string
	Repo     string
	Number   int
	HeadRepo string // head.repo.full_name; may differ from Owner/Repo for forks
	HeadRef  string
	HeadSHA  string // head.sha — immutable across branch deletion; used as Job.Branch git ref
	BaseRef  string
}
```

In `validateAndBuild` (around line 155), populate `HeadSHA` in the state literal:

```go
state := &prReviewState{
	URL:      urlStr,
	Owner:    parts.Owner,
	Repo:     parts.Repo,
	Number:   parts.Number,
	HeadRepo: pr.Head.Repo.FullName,
	HeadRef:  pr.Head.Ref,
	HeadSHA:  pr.Head.SHA,
	BaseRef:  pr.Base.Ref,
}
```

In `BuildJob` (around line 252-273), change `Job.Branch` to use `HeadSHA` while leaving `PromptContext.Branch` on `HeadRef`:

```go
job := &queue.Job{
	ID:          reqID,
	RequestID:   reqID,
	TaskType:    "pr_review",
	ChannelID:   p.ChannelID,
	ThreadTS:    p.ThreadTS,
	UserID:      p.UserID,
	Repo:        st.HeadRepo,
	Branch:      st.HeadSHA, // SHA — immutable git ref consumed by `git worktree add`
	CloneURL:    cloneURL,
	SubmittedAt: time.Now(),
	PromptContext: &queue.PromptContext{
		Branch:           st.HeadRef, // human-readable branch name for prompt
		Goal:             w.cfg.Prompt.PRReview.Goal,
		ResponseSchema:   w.cfg.Prompt.PRReview.ResponseSchema,
		OutputRules:      w.cfg.Prompt.PRReview.OutputRules,
		Language:         w.cfg.Prompt.Language,
		Channel:          p.ChannelName,
		Reporter:         p.Reporter,
		AllowWorkerRules: w.cfg.Prompt.IsWorkerRulesAllowed(),
		// ThreadMessages / Attachments filled by downstream submit-helper.
	},
	WorkflowArgs: map[string]string{
		"pr_url":    st.URL,
		"pr_number": strconv.Itoa(st.Number),
	},
}
```

- [ ] **Step 6: Run all `pr_review_test.go` tests to verify pass + no regression**

```
cd app && go test ./workflow -v
```

Expected: ALL tests PASS, including the two new ones from Steps 1 and 3, and all existing `TestPRReviewWorkflow_*` tests.

- [ ] **Step 7: Commit**

```
git add app/workflow/pr_review.go app/workflow/pr_review_test.go
git commit -m "$(cat <<'EOF'
fix(pr_review): use head SHA for Job.Branch, keep branch name in prompt

PR Review jobs put pr.Head.Ref (branch name) into Job.Branch, which
worker passes to git worktree add. When the PR is merged and the head
branch is deleted on GitHub, the ref no longer exists locally and
prepare fails.

pr.Head.SHA is immutable across branch deletion. Switch Job.Branch to
the SHA; keep PromptContext.Branch on HeadRef so the agent's prompt
still shows the human-readable branch name.

Pairs with the AddWorktree fetch-retry from the previous commit:
together they cover both fresh PR Review jobs (use SHA upfront) and
in-flight queued PR Review jobs serialised before this change (still
recoverable via fetch-retry even if branch gets deleted).

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Verification Across All Tasks

After all three tasks commit:

- [ ] **Run all module tests:**

```
cd shared && go test ./... && cd ../app && go test ./... && cd ../worker && go test ./...
```

Expected: All PASS. The grand total includes Task 1's two new `repo_test.go` tests and Task 3's two new `pr_review_test.go` tests.

- [ ] **Verify import direction not broken:**

```
go test ./test/...
```

Expected: PASS, including `import_direction_test.go`. (No new cross-module imports introduced; everything stays within its own module.)

- [ ] **Inspect commits:**

```
git log --oneline -3
```

Expected: three new commits on top of the spec doc commit, in order: `fix(github)`, `feat(worker)`, `fix(pr_review)`.

---

## Self-Review Notes

**Spec coverage:**
- Goal 1 (PR Review SHA): Task 3 ✓
- Goal 2 (AddWorktree fetch-retry): Task 1 ✓
- Goal 3 (Worker logs Prepare failure): Task 2 ✓
- Non-Goals (no Issue/Ask resolver change, no `EnsureRepo` refspec change, no cancelled-watchdog touch, no PromptContext SHA field, no friendly stderr→prose translation): all preserved — none of the three tasks touches those areas

**Type/signature consistency:**
- `tryAddWorktree(barePath, worktreePath, ref string) error` — used in Task 1 Step 5 in two call sites; signature consistent
- `prReviewState.HeadSHA string` — declared in Task 3 Step 5, populated in same step's `validateAndBuild` change, asserted in Tasks 3 Step 1 + Step 3
- Test fixture `prReviewState{...HeadSHA: "abc..."}` literal uses the same field name as the struct definition

**Placeholder scan:** none. Every step has either exact code or exact commands. No "TBD", no "similar to Task N".

**Out-of-order concern:** Task 1 commits before Task 3, so even if a reviewer cherry-picks only Task 1, the worker is already more resilient before any source-side change references SHAs. Task 2 between them adds visibility regardless of which side the failure originates.
