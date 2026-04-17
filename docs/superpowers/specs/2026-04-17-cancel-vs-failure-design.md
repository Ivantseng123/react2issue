# Distinguish User Cancellation from Agent Failure

**Date:** 2026-04-17
**Status:** Approved
**Issue:** [#36](https://github.com/Ivantseng123/agentdock/issues/36)

## Problem

When a user clicks the "取消" button in Slack, the app sends a `kill` command to the worker, which SIGTERMs the agent process. The agent exits with code 143 (128+15), which the worker currently treats identically to a real agent failure. Consequences:

- Slack shows `:x: 分析失敗: all agents failed` instead of a clean cancellation message.
- A retry button appears, inviting the user to re-run a job they explicitly cancelled.
- Worker log reads `[Worker][完成] 工作完成 status=failed`, obscuring operator visibility.

Root cause: `executeJob` in `internal/worker/executor.go` does not inspect `ctx.Err()` before returning a failed result. The app and worker also lack a dedicated `JobCancelled` status, so every code path converges on `JobFailed`.

A secondary, pre-existing defect surfaced during investigation: `ProcessRegistry.Register` is only called from `OnStarted`, so kill commands issued while the worker is still in the prep phase (attachments, repo clone, skill mount) find no PID and are silently dropped. The user's cancellation takes no effect until the agent eventually starts and later receives SIGTERM — or never, if prep succeeds in one shot and the agent runs to normal completion.

## Goals

1. A user-initiated cancellation produces a clean Slack state (`:white_check_mark: 已取消`), no retry button, and no spurious failure log.
2. Cancellation is honoured at every stage: job still in queue, worker in prep, agent running.
3. Watchdog timeouts and other true failures continue to surface as `failed` with the existing retry affordance.
4. Metrics distinguish cancelled jobs from errored jobs, so the agent failure rate is not polluted.

## Non-Goals

- Introducing a separate `timeout` status. Watchdog timeouts remain `failed` (as today).
- Automatic cancellation based on idle detection or cost caps.
- Letting the user cancel an already-completed job.
- Changing `RepoProvider.Prepare` to accept a `context.Context`. Cancellation during clone waits for the next natural checkpoint to avoid leaving partial repo state.

## Design

### 1. Data Model

**`internal/queue/job.go`** — add a status constant:

```go
const (
    JobPending   JobStatus = "pending"
    JobPreparing JobStatus = "preparing"
    JobRunning   JobStatus = "running"
    JobCompleted JobStatus = "completed"
    JobFailed    JobStatus = "failed"
    JobCancelled JobStatus = "cancelled" // NEW
)
```

`JobResult.Status` gains a `"cancelled"` string value. No boolean flag is added on `JobResult`; a single status field is the source of truth. `JobResult.Error` is left empty for cancelled results (no user-facing error to display).

### 2. Executor Classification

In `internal/worker/executor.go`, all failure returns route through a single classifier that distinguishes user cancellation from other context cancellations (e.g., watchdog kill):

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

The store check is the discriminator: the app sets `JobCancelled` before sending the kill command, so a ctx cancellation with matching store state is a user cancel; any other ctx cancellation (including watchdog kill, which leaves the store at `JobRunning` until after kill) falls through to `failedResult`.

All existing `failedResult(...)` call sites in `executeJob` switch to `classifyResult(...)`:
- attachments resolve failure
- secret key missing / decrypt / unmarshal
- repo prepare
- skill mount
- `runner.Run` return (the original issue site)
- agent output parse

`context.DeadlineExceeded` deliberately falls through to `failedResult` so timeouts remain failures.

### 3. Process Registry Lifecycle

`internal/queue/registry.go` splits registration into two phases so `Kill` works before the agent starts:

```go
// Called at the start of executeWithTracking — stores the jobCancel function only.
func (r *ProcessRegistry) RegisterPending(jobID string, cancel context.CancelFunc) {
    r.mu.Lock()
    defer r.mu.Unlock()
    r.processes[jobID] = &RunningAgent{
        JobID:  jobID,
        cancel: cancel,
        done:   make(chan struct{}),
    }
}

// Called from OnStarted — fills in the process details.
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

`Kill`, `Remove`, and `Get` are unchanged. `Kill` no longer requires a PID to be set; calling `agent.cancel()` is sufficient to propagate cancellation through `ctx` into any `Resolve` / `Prepare` / `Run` call that honours it.

The existing `Register(jobID, pid, command, cancel)` method is removed; no production code outside this call site should use it.

### 4. Pool Integration

`internal/worker/pool.go executeWithTracking`:

```go
jobCtx, jobCancel := context.WithCancel(ctx)
defer jobCancel()

p.registry.RegisterPending(job.ID, jobCancel)
defer p.registry.Remove(job.ID)  // consolidates the explicit Remove at end of function

// ... existing status accumulator and opts setup ...
opts := bot.RunOptions{
    OnStarted: func(pid int, command string) {
        status.setPID(pid, command)
        p.registry.SetStarted(job.ID, pid, command) // was Register(...)
        // ... status reporting start unchanged ...
    },
    // ...
}
```

The manual `p.registry.Remove(job.ID)` at the tail of the function is replaced by the top-level `defer`.

`pool.go runWorker` short-circuit when a job is pulled off the queue after the user already cancelled it:

```go
state, err := p.cfg.Store.Get(job.ID)
if err != nil || state.Status == queue.JobCancelled { // was JobFailed
    p.cfg.Results.Publish(ctx, &queue.JobResult{
        JobID:  job.ID,
        Status: "cancelled", // was "failed"
    })
    continue
}
```

**Race mitigation (small but cheap):** immediately after `RegisterPending`, re-read the store once. If it is already `JobCancelled` (the app set it between queue-pull and registration), call `jobCancel()` so `executeJob`'s classifier returns `cancelled`.

`Prepare(cloneURL, branch, token string)` signature is not changed. If the user cancels during clone, the clone completes (usually seconds), `classifyResult` is called next, sees `ctx.Err() == Canceled` and the cancelled store state, and returns `cancelled`. This trade keeps partial-clone risk out of the picture.

### 5. App-Side Cancel Button

`cmd/agentdock/app.go` cancel handler:

```go
case strings.HasPrefix(action.ActionID, "cancel_job"):
    jobID := action.Value
    state, err := jobStore.Get(jobID)
    if err == nil &&
        state.Status != queue.JobFailed &&
        state.Status != queue.JobCompleted &&
        state.Status != queue.JobCancelled {
        // Order matters: UpdateStatus BEFORE Send so the worker's classifyResult
        // can observe JobCancelled when ctx cancellation propagates.
        jobStore.UpdateStatus(jobID, queue.JobCancelled)
        bundle.Commands.Send(context.Background(), queue.Command{JobID: jobID, Action: "kill"})
        slackClient.UpdateMessage(cb.Channel.ID, selectorTS, ":stop_sign: 正在取消...")
        handler.ClearThreadDedup(cb.Channel.ID, state.Job.ThreadTS)
    } else {
        slackClient.UpdateMessage(cb.Channel.ID, selectorTS, ":information_source: 此任務已結束")
    }
```

The order reversal is load-bearing: the original code does `Send` then `UpdateStatus`, which races against the worker's `classifyResult` store read. Setting `JobCancelled` first and sending `kill` second guarantees the discriminator observes the cancelled state.

The terminal message `:stop_sign: 正在取消...` stays as the intermediate state; the final `:white_check_mark: 已取消` comes from the result listener when the worker confirms.

### 6. Result Listener

`internal/bot/result_listener.go handleResult` gains a cancelled branch before the failed branch:

```go
switch {
case result.Status == "cancelled":
    r.handleCancellation(job, state, result)
case result.Status == "failed":
    r.handleFailure(job, state, result)
case result.Confidence == "low":
    // existing low-confidence path
case result.FilesFound == 0 || result.Questions >= 5:
    // existing degraded issue path
default:
    // existing success path
}
```

New helper:

```go
func (r *ResultListener) handleCancellation(job *queue.Job, state *queue.JobState, result *queue.JobResult) {
    r.store.UpdateStatus(job.ID, queue.JobCancelled)
    r.updateStatus(job, ":white_check_mark: 已取消")
    r.clearDedup(job)
}
```

No retry button, no issue creation, no error log. Dedup is cleared so the user can re-tag the bot in the same thread if they change their mind.

The top-level log line in `handleResult` acquires a cancelled branch so `[Worker][完成] 工作已取消` appears per the issue's expected behaviour.

`recordMetrics` extends the `status` derivation for `AgentExecutionsTotal`:

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

`RetryHandler.Handle` is untouched: it already rejects any status other than `JobFailed`, so a cancelled job is naturally ineligible for retry even if someone crafts a retry payload.

### 7. Watchdog Alignment

`internal/queue/watchdog.go` must not overwrite a cancelled job with a timeout failure.

In `check()`:

```go
if state.Status == JobCompleted || state.Status == JobFailed || state.Status == JobCancelled {
    continue
}
```

In `killAndPublish()`, re-read the store after sending the kill command but before mutating state. If the user cancelled between `check()` and this point, back off and let the worker publish the cancelled result:

```go
fresh, _ := w.store.Get(state.Job.ID)
if fresh != nil && fresh.Status == JobCancelled {
    return
}
// existing UpdateStatus(JobFailed) + Publish failed result
```

When the watchdog legitimately times out a job, it proceeds as today. The worker's executor sees `ctx.Err() == Canceled` and a store status of `JobFailed`, so `classifyResult` returns `failed`. The `ResultListener.processedJobs` dedup map drops whichever of {watchdog-published failed, worker-published failed} arrives second.

## Testing

### Unit

**`internal/worker/executor_test.go`** (new if absent):
- `classifyResult`: ctx.Canceled + store `JobCancelled` → `cancelled`.
- `classifyResult`: ctx.Canceled + store `JobRunning` → `failed` (watchdog-kill path).
- `classifyResult`: ctx.DeadlineExceeded + any store state → `failed`.
- `classifyResult`: err non-nil, ctx OK → `failed`.

**`internal/queue/registry_test.go`**:
- `RegisterPending` then `Kill` cancels the context without needing `SetStarted`.
- `RegisterPending` → `SetStarted` → `Kill` still records PID / Command correctly.
- `Kill` on an unknown job still returns the existing error.

**`internal/bot/result_listener_test.go`**:
- `result.Status == "cancelled"` → store `JobCancelled`, Slack updated to `:white_check_mark: 已取消`, dedup cleared, no retry button posted.
- Metrics emit `AgentExecutionsTotal{status="cancelled"}`.

**`internal/queue/watchdog_test.go`**:
- A `JobCancelled` state past its timeout is skipped by `check()`.
- `killAndPublish` observing a `JobCancelled` state in the re-read phase exits without UpdateStatus or Publish.

### Integration (`internal/worker/pool_test.go`)

Use a controllable fake `Runner` to exercise timing:

1. **Scenario A — queue hit post-cancel:** Put job, set store to `JobCancelled` before the worker pulls. Assert the published result is `cancelled`.
2. **Scenario B — prep cancel:** Runner sleeps during prep simulation; call `registry.Kill`. Assert `executeJob` exits with a `cancelled` result.
3. **Scenario C — agent-running cancel:** Runner enters `Run` then blocks; call `registry.Kill`. Assert `cancelled` result.
4. **Watchdog regression:** Simulate a watchdog kill (store `JobFailed`, ctx cancel). Assert the result is `failed`, not cancelled.

### Manual smoke

- `make dev` or equivalent: run app + worker locally, click cancel in Slack mid-agent, confirm:
  - Worker log line `[Worker][完成] 工作已取消`.
  - Slack final message `:white_check_mark: 已取消` with no retry button.
  - Re-tagging the bot in the same thread works (dedup cleared).

## Files Changed

- `internal/queue/job.go` — add `JobCancelled`.
- `internal/queue/registry.go` — split `Register` into `RegisterPending` + `SetStarted`; remove `Register`.
- `internal/queue/registry_test.go` — migrate existing `Register(...)` call sites to `RegisterPending` + `SetStarted`; add cancel-before-start assertions.
- `internal/queue/watchdog.go` — skip cancelled in `check`, back off in `killAndPublish`.
- `internal/queue/watchdog_test.go` — new assertions.
- `internal/worker/executor.go` — `classifyResult` + `cancelledResult`, all failure sites.
- `internal/worker/executor_test.go` — new.
- `internal/worker/pool.go` — `RegisterPending` / `SetStarted` wiring, queue short-circuit to cancelled, optional race re-read.
- `internal/worker/pool_test.go` — three scenarios + watchdog regression.
- `internal/bot/result_listener.go` — `handleCancellation`, metrics branch, log branch.
- `internal/bot/result_listener_test.go` — cancellation assertions.
- `cmd/agentdock/app.go` — cancel button sets `JobCancelled`, guard extended.

## Risks

- **Incomplete clone on cancel during prep.** We deliberately let clone finish instead of wiring ctx through `Prepare`. Worst case: user waits a few extra seconds for their cancel to land; no partial-state footgun.
- **Watchdog / user-cancel race.** Mitigated by both the skip-list update in `check` and the re-read in `killAndPublish`. If both fire simultaneously, dedup in `ResultListener` drops the loser.
- **Redis-backed store lag.** `classifyResult`'s discriminator is a store read. In Redis deployments, a stale read could classify a user-cancel as failed. Mitigation: the app's `UpdateStatus(JobCancelled)` happens before `Commands.Send("kill")`, so a worker receiving the kill must already be able to see the state unless Redis replication lag exceeds the command-bus path (unlikely; both traverse the same Redis).
