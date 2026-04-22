# Worker Liveness Precheck (Trigger-time Warn + Submit-time Reject)

**Date:** 2026-04-22
**Status:** Draft

## Problem

When a Slack user `@bot`-triggers a triage, `app/bot/workflow.go` immediately posts `:mag: 正在排入處理佇列...` and proceeds through repo/branch/description selection, then enqueues the job. The bot **never checks whether any worker is online**.

Failure mode: with zero registered workers (or worker pool fully busy with a saturated queue), the job sits in the Redis stream until `shared/queue/watchdog.go` kills it after `JobTimeout`. The user sees only `:hourglass_flowing_sand: 正在處理你的請求...` and waits — with no signal that nothing will happen and no clue what to do next.

The `shared/queue` package already has the primitives needed:

- `JobQueue.ListWorkers(ctx)` returns 30s-TTL worker registrations (`shared/queue/redis_jobqueue.go:202`).
- `JobQueue.QueueDepth()` returns Redis stream length.
- `JobStore.ListAll()` enumerates job states.

No code calls these to gate the trigger flow. This spec wires them in.

This spec also addresses a forward-compatibility constraint: the bot is expected to grow new mediums beyond Slack (X, Discord, etc.). The precheck logic must be reusable across mediums without each medium re-implementing the rule.

## Goals

1. When a user triggers triage and **no workers** are online:
   - Post a soft warning in the thread at trigger time (do not block selection).
   - Hard-reject at submit time before any queue interaction; clear thread dedup so the user can retry.
2. When workers exist but **capacity is saturated** (queued + running ≥ total slots):
   - Allow enqueue; surface an estimated wait alongside the existing `:hourglass:` lifecycle message.
3. Precheck rule lives in `shared/queue/`, agnostic of medium. Each medium translates the verdict into its own user-visible message.
4. Precheck failure of dependencies (Redis SCAN/XLEN) is fail-open: log and treat as `OK` — never let availability checks themselves break triage.
5. Worker capacity is modelled with a per-worker `Slots` field (default 1) so that future concurrent workers do not require a schema change.

## Non-Goals

- Auto-notifying oncall when workers are absent (deferred; messaging extension point preserved).
- Historical sliding-window ETA averaging — a static `AvgJobDuration` constant is used.
- Caching verdicts (deferred; soft check is currently uncached).
- Implementing a second medium adapter. Only the abstraction shape is laid down.
- Changing how workers register or how their TTL is renewed.
- Distinguishing per-agent capability (e.g. "no `claude` worker"). Treats all workers as fungible.

## Design

### 1. Data Model

**`shared/queue/job.go`** — extend `WorkerInfo` with a `Slots` field:

```go
type WorkerInfo struct {
    WorkerID    string   `json:"worker_id"`
    Name        string   `json:"name"`
    Nickname    string   `json:"nickname,omitempty"`
    Agents      []string `json:"agents"`
    Tags        []string `json:"tags"`
    Slots       int      `json:"slots,omitempty"` // NEW — concurrent jobs this worker can handle; 0 normalised to 1
    ConnectedAt time.Time
}
```

Backward compatible: existing workers omitting the field deserialise to `Slots: 0`, normalised to `1` at consumption time. Worker code populates `Slots` from its config (initially always `1` for today's single-job-per-worker pool).

### 2. Availability Service

New file **`shared/queue/availability.go`**:

```go
type VerdictKind string

const (
    VerdictOK            VerdictKind = "ok"            // healthy
    VerdictBusyEnqueueOK VerdictKind = "busy_enqueue"  // workers exist but saturated
    VerdictNoWorkers     VerdictKind = "no_workers"    // no online workers
)

type Verdict struct {
    Kind          VerdictKind
    WorkerCount   int
    ActiveJobs    int           // QueueDepth + count of preparing/running JobStates
    TotalSlots    int           // Σ max(WorkerInfo.Slots, 1)
    EstimatedWait time.Duration // populated only for VerdictBusyEnqueueOK
}

type WorkerAvailability interface {
    CheckSoft(ctx context.Context) Verdict
    CheckHard(ctx context.Context) Verdict
}

type AvailabilityConfig struct {
    AvgJobDuration time.Duration // used for ETA; default 3m if zero
}
```

`CheckSoft` and `CheckHard` currently delegate to a private `compute(ctx)` returning the same `Verdict`. They are intentionally separate methods so that future tweaks (e.g. caching `CheckSoft`) need not touch `CheckHard`.

**Computation:**

```
depErr := false

workers, err := ListWorkers()
if err != nil { log.Warn(...); depErr = true; workers = nil }

states, err := ListAll()
if err != nil { log.Warn(...); depErr = true; states = nil }

depth, err := QueueDepth()
if err != nil { log.Warn(...); depErr = true; depth = 0 }

// Fail-open: any dependency error → return OK rather than mis-classify.
// Treat partial blindness as healthy; the existing watchdog still backstops.
if depErr {
    return {Kind: OK, ...}
}

totalSlots := Σ normaliseSlots(w.Slots) for w in workers   // 0 → 1
activeJobs := depth + |{s : s.Status ∈ {preparing, running}}|

if len(workers) == 0:
    return {Kind: NoWorkers, ...}
if activeJobs >= totalSlots:
    overflow := activeJobs - totalSlots + 1
    return {Kind: BusyEnqueueOK, EstimatedWait: overflow * AvgJobDuration, ...}
return {Kind: OK, ...}
```

The fail-open early-return is the single source of truth for dependency-error behaviour. `WorkerAvailabilityCheckErrors` is incremented per failing dependency before returning.

### 3. Slack Adapter Integration

**`app/bot/workflow.go`** changes:

1. Inject availability via constructor:

```go
func NewWorkflow(
    cfg *config.Config,
    slack slackAPI,
    repoCache *ghclient.RepoCache,
    repoDiscovery *ghclient.RepoDiscovery,
    jobQueue queue.JobQueue,
    jobStore queue.JobStore,
    attachStore queue.AttachmentStore,
    resultBus queue.ResultBus,
    skillProvider SkillProvider,
    identity Identity,
    availability queue.WorkerAvailability, // NEW
) *Workflow
```

2. **Trigger-time soft warn** in `HandleTrigger`, after the channel-guard block but before `repo` selection. `HandleTrigger` does not currently take a `context.Context`; use `context.Background()` to keep this spec's scope tight:

```go
verdict := w.availability.CheckSoft(context.Background())
if verdict.Kind == queue.VerdictNoWorkers {
    w.slack.PostMessage(event.ChannelID,
        RenderSoftWarn(verdict), event.ThreadTS)
    // Do NOT return — selection still proceeds; hard check will gate submit.
}
```

3. **Submit-time hard check** in `runTriage`, immediately after the existing `ctx := context.Background()` (line 387) and **before** the `:mag: 正在排入處理佇列...` post (line 392):

```go
verdict := w.availability.CheckHard(ctx)
switch verdict.Kind {
case queue.VerdictNoWorkers:
    w.slack.PostMessage(pt.ChannelID,
        RenderHardReject(verdict), pt.ThreadTS)
    w.clearDedup(pt)
    return
case queue.VerdictBusyEnqueueOK:
    pt.busyHint = RenderBusyHint(verdict) // attached to lifecycle message
case queue.VerdictOK:
    // continue
}
```

`pendingTriage` gains an unexported `busyHint string` field. The existing block that builds the post-submit `statusMsg` (`workflow.go:537–544`) is amended:

```go
if pos <= 1 {
    statusMsg = ":hourglass_flowing_sand: 正在處理你的請求..."
} else {
    statusMsg = fmt.Sprintf(":hourglass_flowing_sand: 已加入排隊，前面有 %d 個請求", pos-1)
}
if pt.busyHint != "" {
    statusMsg += " " + pt.busyHint
}
```

### 4. Verdict Rendering

New file **`app/bot/verdict_message.go`**:

```go
func RenderSoftWarn(v queue.Verdict) string {
    return ":warning: 目前沒有 worker 在線，你仍可繼續選擇，送出時會再確認一次。"
}

func RenderHardReject(v queue.Verdict) string {
    return ":x: 目前沒有 worker 在線，無法處理。請稍後再試。"
}

func RenderBusyHint(v queue.Verdict) string {
    if v.EstimatedWait <= 0 {
        return ""
    }
    return fmt.Sprintf("(預估等候 ~%dm)",
        int(v.EstimatedWait.Round(time.Minute).Minutes()))
}
```

This file is the single point of change when (a) oncall handles need to be appended to reject messages or (b) verdict text needs to be localised. The `Verdict` struct is the contract; future medium adapters write their own renderers without touching `shared/queue/availability.go`.

### 5. Wiring

**`app/app.go`** — construct availability after `coordinator` and `jobStore` exist (currently around `metrics.Register` at line 184), then pass into `NewWorkflow`:

```go
availability := queue.NewWorkerAvailability(coordinator, jobStore, queue.AvailabilityConfig{
    AvgJobDuration: cfg.Availability.AvgJobDuration,
})
workflow := bot.NewWorkflow(..., availability)
```

**`app/config/`** — add an `Availability` block to `app.yaml` schema:

```yaml
availability:
  avg_job_duration: 3m
```

Field is optional; absent → service default of `3 * time.Minute`.

### 6. Worker-side Change

**`worker/worker.go`** — when calling `JobQueue.Register`, populate `Slots`:

```go
info := queue.WorkerInfo{
    WorkerID: ...,
    Slots:    1, // hardcoded today; future: read from worker.yaml
    ...
}
```

No `worker.yaml` schema change in this spec — the field is hardcoded to keep scope tight. A follow-up can introduce `worker.slots` config when concurrent execution is implemented.

### 7. Metrics

Add to **`shared/metrics/`**:

```go
WorkerAvailabilityVerdictTotal *prometheus.CounterVec  // labels: kind, stage
WorkerAvailabilityCheckDuration prometheus.Histogram
WorkerAvailabilityCheckErrors *prometheus.CounterVec   // labels: dependency
```

Increment at the end of every `compute()` call. Sufficient to answer: how often does each verdict fire, how long does the check take, how often does each dependency fail.

### 8. Error Handling Summary

| Failure | Behaviour |
|---|---|
| `coordinator.ListWorkers` errors | Logged warn; verdict forced to `OK` (fail-open) |
| `jobStore.ListAll` errors | Logged warn; verdict forced to `OK` |
| `jobQueue.QueueDepth` errors | Logged warn; verdict forced to `OK` |
| Slack `PostMessage` (soft warn) fails | Logged warn; flow continues; hard check still runs |
| Slack `PostMessage` (hard reject) fails | Logged error; **flow still terminates** (do not enqueue) |
| Panic inside `compute()` | `recover` returns zero-value verdict (`OK`); logged error |

Explicitly **not** doing: retry, circuit breaker, verdict caching. All YAGNI for current load.

### 9. Ordering Constraints

- Soft check runs **after** `channel-guard` (`autoBound` / `AutoBind`) — never broadcast worker status to channels the bot is not bound to.
- Soft check runs **after** `event.ThreadTS == ""` guard — outside-thread triggers get the existing usage hint, not a worker status.
- Hard check runs **before** the first lifecycle `PostMessage` in `runTriage` — never leave a `":mag:"` orphan when rejecting.
- Hard reject **must** call `clearDedup(pt)` — otherwise the user's retry within `DedupTTL` (5m default) is silently swallowed.

## Testing

### Unit — `shared/queue/availability_test.go`

Drive `WorkerAvailability` against the in-memory `queuetest` bundle:

| # | Workers (slots) | Active jobs | Expected verdict |
|---|---|---|---|
| 1 | 2 (1, 1) | 1 | `OK` |
| 2 | 2 (1, 1) | 2 | `BusyEnqueueOK`, ETA = 1 × AvgJobDuration |
| 3 | 2 (1, 1) | 5 | `BusyEnqueueOK`, ETA = 4 × AvgJobDuration |
| 4 | 0 | 0 | `NoWorkers` |
| 5 | 1 (3) | 2 | `OK` (sums to 3 slots) |
| 6 | 1 (0) | 0 | `OK` (normalised to 1) |
| 7 | `ListWorkers` errors | n/a | `OK` (fail-open) |
| 8 | `ListAll` errors | n/a | `OK` (fail-open) |
| 9 | `QueueDepth` errors | n/a | `OK` (fail-open) |

### Integration — `app/bot/workflow_test.go`

Add three cases using the existing `slackAPI` stub and a stub `WorkerAvailability`:

- `TestHandleTrigger_NoWorkers_PostsSoftWarnButContinues` — assert: soft warn message posted; repo selector still appears.
- `TestRunTriage_NoWorkers_HardRejects` — assert: hard reject message posted; no `queue.Submit`; no lifecycle message; `clearDedup` invoked.
- `TestRunTriage_BusyEnqueueOK_LifecycleMessageIncludesETA` — assert: `queue.Submit` called; lifecycle message contains `預估等候`.

### Out of Scope

- End-to-end Slack live tests (mocked stubs cover the surface).
- Multi-app race tests for verdict consistency (Redis is the central source of truth — covered by existing `redis_jobqueue_test.go`).
- Worker register/expire timing tests (already covered by `redis_jobqueue_test.go`).

## Migration / Rollout

1. Code change is additive: `WorkerInfo.Slots` is omit-empty JSON; old workers continue to register without it.
2. Existing workflows run unchanged when verdict is `OK` (the only path observable today on a healthy deployment).
3. No config migration required — `availability.avg_job_duration` is optional.

## Open Questions

None. All design decisions confirmed during brainstorming on 2026-04-22.
