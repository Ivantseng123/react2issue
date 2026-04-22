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

### 3. Slack Adapter Integration (dispatcher architecture)

The bot has a thin Slack-side shim (`app/bot/workflow.go`) that delegates real logic to a `workflow.Dispatcher` and one of three workflows (`issue`, `ask`, `pr_review`) via the `app/workflow` package. Each workflow returns a `NextStep` of various kinds; `executeStep` (in the shim) calls a closure `onSubmit` (set by `app/app.go` via `SetSubmitHook`) when the workflow returns `NextStepSubmit` (or fallback paths in `NextStepOpenModal`). Today three call sites in `executeStep` reach `onSubmit`.

**`app/bot/workflow.go`** changes:

1. Add `availability` field + extend constructor:

```go
type Workflow struct {
    cfg           *config.Config
    dispatcher    *workflow.Dispatcher
    slack         workflow.SlackPort
    handler       *slackclient.Handler
    repoDiscovery *ghclient.RepoDiscovery
    logger        *slog.Logger
    availability  queue.WorkerAvailability // NEW
    // ...
}

func NewWorkflow(
    cfg *config.Config,
    dispatcher *workflow.Dispatcher,
    slack workflow.SlackPort,
    repoDiscovery *ghclient.RepoDiscovery,
    logger *slog.Logger,
    availability queue.WorkerAvailability, // NEW
) *Workflow
```

2. **Trigger-time soft warn** in `HandleTrigger`, after the channel-binding check (`if _, ok := w.cfg.Channels[event.ChannelID]; !ok { ... }`) and BEFORE `ctx := context.Background()` / `dispatcher.Dispatch`:

```go
if w.availability != nil {
    verdict := w.availability.CheckSoft(context.Background())
    if verdict.Kind == queue.VerdictNoWorkers {
        _ = w.slack.PostMessage(event.ChannelID,
            RenderSoftWarn(verdict), event.ThreadTS)
        // Do NOT return — selection still proceeds; hard check at submit gates the submission.
    }
}
```

3. **Submit-time hard check** in a new `submit()` helper that consolidates the three current `onSubmit` call sites in `executeStep`:

```go
// submit is the single chokepoint for sending a Pending to the queue-submission
// closure. Replacing 3 onSubmit call sites in executeStep with this helper means
// future pre-submit checks (rate limit, quota, etc.) only need to be added once.
func (w *Workflow) submit(ctx context.Context, p *workflow.Pending) {
    if w.availability != nil {
        verdict := w.availability.CheckHard(ctx)
        switch verdict.Kind {
        case queue.VerdictNoWorkers:
            _ = w.slack.PostMessage(p.ChannelID,
                RenderHardReject(verdict), p.ThreadTS)
            if w.handler != nil {
                w.handler.ClearThreadDedup(p.ChannelID, p.ThreadTS)
            }
            return
        case queue.VerdictBusyEnqueueOK:
            p.BusyHint = RenderBusyHint(verdict)
        case queue.VerdictOK:
            // continue
        }
    }
    if w.onSubmit != nil {
        w.onSubmit(ctx, p)
    } else {
        w.logger.Warn("submit but no onSubmit hook set", "phase", "失敗")
    }
}
```

The three `executeStep` call sites that today read `if w.onSubmit != nil { w.onSubmit(ctx, ...) }` collapse to a single `w.submit(ctx, ...)`. (The `nil` check moves into `submit()`.)

**`app/workflow/workflow.go`** changes:

`Pending` gains an exported `BusyHint string` field (cross-package: shim sets it, `app/app.go` `submitJob` reads it):

```go
type Pending struct {
    // ... existing fields ...
    State    any    // per-workflow state struct
    BusyHint string // populated by the shim's submit() when verdict is BusyEnqueueOK; appended to lifecycle status text
}
```

**`app/app.go`** `submitJob` closure changes:

After `BuildJob` returns `statusText`, append the busy hint before posting the lifecycle status message:

```go
job, statusText, err := wfImpl.BuildJob(ctx, p)
if err != nil { /* ... */ return }

if p.BusyHint != "" {
    statusText += " " + p.BusyHint
}

statusMsgTS, postErr := slackPort.PostMessageWithTS(p.ChannelID, statusText, p.ThreadTS)
```

Two-line insertion only; no other changes to `submitJob`.

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

**`app/app.go`** — construct `availability` after `coordinator` and `jobStore` are built (around the existing dispatcher construction, ~line 168), then pass into `bot.NewWorkflow`:

```go
availability := queue.NewWorkerAvailability(coordinator, jobStore, queue.AvailabilityConfig{
    AvgJobDuration: cfg.Availability.AvgJobDuration,
},
    queue.WithVerdictHook(func(kind, stage string, d time.Duration) {
        metrics.WorkerAvailabilityVerdictTotal.WithLabelValues(kind, stage).Inc()
        metrics.WorkerAvailabilityCheckDuration.Observe(d.Seconds())
    }),
    queue.WithDepErrorHook(func(dep string) {
        metrics.WorkerAvailabilityCheckErrors.WithLabelValues(dep).Inc()
    }),
)

wf := bot.NewWorkflow(cfg, dispatcher, slackPort, repoDiscovery, appLogger, availability)
```

**`app/config/`** — add an `Availability` block to `app.yaml` schema:

```yaml
availability:
  avg_job_duration: 3m
```

Field is optional; absent → service default of `3 * time.Minute`.

### 6. Worker-side Change

**`worker/pool/pool.go`** — `workerHeartbeat` (around line 241) builds `WorkerInfo` in two places (initial registration and the 20s ticker re-registration). Both literals get a `Slots: 1`:

```go
info := queue.WorkerInfo{
    WorkerID:    fmt.Sprintf("%s/worker-%d", p.cfg.Hostname, i),
    Name:        p.cfg.Hostname,
    Nickname:    p.nicknameForIndex(i),
    Slots:       1, // hardcoded; future: read from worker.yaml when concurrent execution lands
    ConnectedAt: now,
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
- Hard check runs **inside the `submit()` helper, before** any call to `onSubmit` — never let a job reach `submitJob` (which posts the `:mag:` lifecycle text) when verdict is `NoWorkers`.
- Hard reject **must** call `handler.ClearThreadDedup(p.ChannelID, p.ThreadTS)` — otherwise the user's retry within `DedupTTL` (5m default) is silently swallowed.
- BusyHint append in `submitJob` runs **after** `BuildJob` and **before** `slackPort.PostMessageWithTS` — appending later (after Post) would require an additional Slack edit call.

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

Add three cases using the existing `shimSlack` and `fakeIssueWorkflow` test fixtures and a stub `WorkerAvailability`:

- `TestHandleTrigger_NoWorkers_PostsSoftWarnButContinues` — assert: soft warn message posted; `dispatcher.Dispatch` was still reached (e.g. assert a follow-on selector post or `submitHook` invocation).
- `TestSubmit_NoWorkers_HardRejects` — drive `executeStep` with `NextStepSubmit` and a stub availability returning `NoWorkers`; assert: hard reject posted, `handler.ClearThreadDedup` invoked, `onSubmit` **not** called.
- `TestSubmit_BusyEnqueueOK_SetsBusyHint` — drive `executeStep` with `NextStepSubmit` and a stub availability returning `BusyEnqueueOK`; assert: `onSubmit` **was** called, the `*workflow.Pending` arg has `BusyHint != ""` containing `預估等候`.

(End-to-end "lifecycle message contains ETA" is split: the shim's responsibility is setting `Pending.BusyHint`; the closure in `app/app.go` appends it to `statusText`. Each side is testable in isolation; an integration test of the closure can live in a separate file if desired but is not in this spec.)

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
