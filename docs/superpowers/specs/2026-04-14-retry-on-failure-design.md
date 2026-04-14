# Retry on Failure Design

**Date:** 2026-04-14
**Status:** Approved

## Problem

When a worker crashes, times out, or an agent fails mid-execution, the app relies on watchdog timeout detection to notify the user via Slack. The user must then manually re-tag the bot to retry. This is slow (waits for full timeout) and friction-heavy (requires re-triggering the entire workflow).

## Goals

1. All failure types (watchdog kill, agent error, infra error) are retryable via a Slack button.
2. Failure reason is visible in the Slack message.
3. Maximum 1 retry per job. Second failure shows error only, no button.
4. Re-tagging the bot remains an independent path (re-reads thread context, re-selects repo).
5. Worker identity (hostname + index) is visible in status and failure messages.

## Non-Goals

- Automatic retry without user action.
- Retry count > 1.
- `WORKER_NAME` environment variable override.
- Re-queuing via Redis pending entry list (PEL) recovery.

## Design

### 1. Job Model Changes

Add two fields to `Job`:

```go
type Job struct {
    // ... existing fields
    RetryCount   int    `json:"retry_count"`      // 0 = first attempt, 1 = retried
    RetryOfJobID string `json:"retry_of_job_id"`  // original job ID for tracing
}
```

Retry creates a **new Job** (new ID) copying the original job's prompt, repo, thread context, branch, attachments, user ID, and skills. `RetryCount` is set to `original + 1`.

### 2. Watchdog Publishes to ResultBus

Current flow: Watchdog detects timeout → kills agent → calls `StuckNotifier` callback → callback posts directly to Slack.

New flow: Watchdog detects timeout → kills agent → publishes a `failed` result to `ResultBus`.

Changes to `Watchdog`:
- Remove `StuckNotifier` type and the `notifier` field.
- Add `ResultBus` dependency.
- `killAndNotify()` publishes a `JobResult{Status: "failed", Error: "job terminated: <reason>"}` instead of calling the notifier.
- Remove `FormatStuckMessage()` helper (dead code after this change).

**Double-publish guard:** When watchdog kills a job, both the watchdog and the worker may publish a failed result to `ResultBus` (watchdog publishes immediately; worker publishes after context cancellation causes `executeJob` to return). `ResultListener` guards against this: on receiving a failed result, it checks `JobState.Status`. If the job is already in `JobFailed` or `JobCompleted` state, the result is silently dropped. This is set via `UpdateStatus(jobID, JobFailed)` on first processing.

This same guard also prevents stale results from interfering after a retry button click (see Section 4).

### 3. ResultListener Unified Failure Handling

`ResultListener.handleResult()` on `status == "failed"`:

```
if job status already terminal (JobFailed/JobCompleted):
    drop (duplicate result from watchdog + worker race)
    return

mark job as JobFailed in store

if job.RetryCount < 1:
    post failure message WITH retry button
else:
    post failure message WITHOUT button (text indicates retry was attempted)

clear thread dedup (allow re-tag bot in same thread)
```

Message format with button:
```
:x: 分析失敗: <error reason>
repo: owner/repo | worker: Ivans-MacBook-Pro/worker-0

[🔄 重試]   ← Slack button (action_id: "retry_job", value: job.ID)
```

Message format after retry exhausted:
```
:x: 分析失敗（重試後仍失敗）: <error reason>
repo: owner/repo | worker: a1b2c3d4/worker-0
```

**Interface changes:** `SlackPoster` gains a new method for posting messages with blocks:

```go
type SlackPoster interface {
    PostMessage(channelID, text, threadTS string)
    UpdateMessage(channelID, messageTS, text string)
    PostBlocks(channelID, threadTS string, blocks []slack.Block) (string, error)  // new
}
```

This requires updates in three places:
- `internal/bot/result_listener.go` — interface definition
- `internal/slack/client.go` — concrete `Client` implementation
- `cmd/bot/main.go` — `slackPosterAdapter` (if one exists) or wiring

**Thread dedup:** `ResultListener` needs a `ClearThreadDedup` callback (or dependency on handler) to clear dedup on failure. Without this, re-tagging the bot after failure would be blocked. Currently `ClearThreadDedup` is called in the watchdog notifier callback in `main.go`; this responsibility moves to `ResultListener`.

`ResultListener` reads `JobState.WorkerID` from `JobStore` to include worker identity in the message. In Redis mode, `WorkerID` reaches the bot's `JobStore` via the `StatusListener` path: worker sends `StatusReport` (with `WorkerID`) → `StatusBus` → `StatusListener` calls `SetAgentStatus()` which stores `WorkerID` in the `StatusReport` field of `JobState`. `ResultListener` reads it from `state.AgentStatus.WorkerID`.

### 4. Retry Button Interaction Handler

New file: `internal/bot/retry_handler.go`

**Dependencies:**
- `JobStore` — lookup original job, `Put()` new job
- `JobQueue` (or `Coordinator`) — `Submit()` new job
- `SlackPoster` — update message

Handles `block_actions` interaction with `action_id: "retry_job"`:

1. Look up original job from `JobStore` using `value` (job ID). If not found (TTL expired), post error message and return.
2. If original job is not in `JobFailed` state (e.g., user clicks stale button on a job that already completed), ignore.
3. Update the original failure message to `:arrows_counterclockwise: 重試中，已重新排入佇列...` (button removed).
4. Create new `Job` copying: `Prompt`, `Repo`, `CloneURL`, `Branch`, `ChannelID`, `ThreadTS`, `UserID`, `Attachments`, `Skills`, `Priority`. Set `RetryCount = original + 1`, `RetryOfJobID = original.ID`.
5. Submit new job to queue.
6. Set `StatusMsgTS` on the new job to the same message timestamp, so subsequent status updates overwrite it.

Routing: `internal/slack/handler.go` routes `block_actions` with `action_id == "retry_job"` to the retry handler.

### 5. Coexistence with Re-tag Bot

Re-tagging the bot in the same thread triggers the full workflow: re-reads all thread messages, presents repo/branch selector, creates a brand new job. This is completely independent of the retry mechanism.

No conflict because:
- Retry creates a new job with a new ID.
- Thread dedup is cleared on failure (see Section 3). The original failed job is in `JobFailed` status, so it won't block a new trigger.

### 6. Worker Identity

Worker ID format: `<hostname>/worker-<index>`

- Native: `Ivans-MacBook-Pro/worker-0`
- Docker: `a1b2c3d4/worker-0` (container short ID from `os.Hostname()`)

Where it's used:
- `StatusReport.WorkerID` — already exists, currently only `worker-0`. Change to include hostname.
- `JobState.WorkerID` — already exists but `SetWorker()` is never called today. Add call in pool after Ack.
- In Redis mode, `WorkerID` propagates to the bot via `StatusReport` → `StatusBus` → `StatusListener` → `SetAgentStatus()`. The bot reads it from `state.AgentStatus.WorkerID`.
- Failure messages in Slack — `ResultListener` reads from `JobState`.

Implementation: `Pool` receives hostname at construction time (`cmd/bot/worker.go` calls `os.Hostname()`). Each worker goroutine uses `<hostname>/worker-<index>`.

## Data Flow

```
First attempt:
  trigger → workflow → Submit(Job{RetryCount:0}) → worker executes
    → success: ResultListener → create issue → post URL
    → failure: ResultListener → guard (first result wins) → post error + retry button + clear dedup

Watchdog timeout:
  watchdog → kill command + publish failed result to ResultBus
  worker → context cancelled → also publishes failed result
  ResultListener → first result processed, second dropped by terminal-state guard

User clicks retry:
  interaction handler → check job is in JobFailed state
    → update message to "retrying..."
    → Submit(Job{RetryCount:1, RetryOfJobID:original}) → worker executes
      → success: ResultListener → update same message to issue URL
      → failure: ResultListener → update same message to error (no button) + clear dedup

User re-tags bot:
  independent new workflow → new thread context → repo selection → new job
  (works because dedup was cleared on failure)
```

## Files Changed

| File | Change |
|------|--------|
| `internal/queue/job.go` | Add `RetryCount`, `RetryOfJobID` to `Job` |
| `internal/queue/watchdog.go` | Remove `StuckNotifier`, `FormatStuckMessage`; add `ResultBus`; `killAndNotify` publishes result |
| `cmd/bot/main.go` | Update Watchdog wiring: remove notifier callback, pass ResultBus; update `slackPosterAdapter` if needed |
| `internal/bot/result_listener.go` | Add terminal-state guard; failure handling with RetryCount; post blocks; clear thread dedup; read WorkerID |
| `internal/slack/client.go` | Add `PostBlocks` concrete implementation |
| `internal/bot/retry_handler.go` | New: retry button interaction handler (deps: JobStore, JobQueue, SlackPoster) |
| `internal/slack/handler.go` | Route `block_actions` `retry_job` to retry handler |
| `internal/worker/pool.go` | Worker ID uses hostname/index; call `SetWorker()` after Ack |
| `cmd/bot/worker.go` | Pass `os.Hostname()` to Pool at startup |

## Testing

- Unit test: `ResultListener` posts button when `RetryCount == 0`, no button when `RetryCount == 1`.
- Unit test: `ResultListener` drops duplicate result for a job already in terminal state (double-publish guard).
- Unit test: `ResultListener` calls `ClearThreadDedup` on failure.
- Unit test: retry handler creates new job with correct fields (`UserID`, `RetryCount + 1`, etc.).
- Unit test: retry handler ignores click if job is not in `JobFailed` state (stale button).
- Unit test: retry handler returns graceful error if job not found (TTL expired).
- Unit test: Watchdog publishes failed result to ResultBus (no longer calls notifier).
- Unit test: worker ID format includes hostname.
- Integration: trigger failure → see button → click retry → job re-executes.
