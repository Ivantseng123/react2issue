# Status Message Progress Visibility

**Date:** 2026-04-17
**Status:** Approved

## Problem

Triage jobs routinely take 5+ minutes to complete. During that window, the user's only Slack status message reads `:hourglass_flowing_sand: 正在處理你的請求...` — unchanged from submission to completion. Two concrete user complaints:

1. **No worker attribution.** The user cannot tell which worker picked up their job. For operators debugging a hung pool, this means jumping to admin endpoints / logs to correlate thread → worker.
2. **No liveness signal.** Five minutes of a static "processing" message leaves the user guessing whether the job is truly running or silently stuck. Users resort to re-triggering or asking in chat.

Data actually flows — the worker publishes periodic `StatusReport` every 5 seconds (`pool.go:273 reportStatus`), and `StatusListener` already persists each report to the job store via `SetAgentStatus`. The missing piece is a Slack surface: the status message is never updated until the job terminates (via `ResultListener.UpdateMessage` in `result_listener.go:309`).

## Goals

1. The status message shows the assigned worker ID once a worker picks up the job (preparing phase).
2. During the running phase, the message updates at least every 15 seconds with elapsed time — so the "seconds ticking" visual is itself the liveness signal.
3. For agents with streaming (`claude`), additionally show tool-call / file-read counters as a second line. For non-streaming agents (`codex`, `opencode`), this line is automatically omitted (data-driven, no per-agent branching).
4. The cancel button on the status message is preserved across all updates.
5. Final terminal states (completed / failed / cancelled) are handled exclusively by `ResultListener` — no cross-listener race.

## Non-Goals

- **Dynamic queue-position updates.** The current `已加入排隊，前面有 N 個請求` message is posted once at submit time and never updates as jobs ahead finish. Adding that requires a fan-out broadcast from result completion to every pending job's status message — out of scope.
- **Per-agent hardcoded templates.** Display adapts to the data in `StatusReport`, not to `AgentCmd` string matching. `ToolCalls > 0` is the trigger for the stats line, not `agentCmd == "claude"`.
- **Surfacing cost / token usage to end users.** Cost and tokens are operator data; they remain in the store and Grafana dashboards, not in Slack thread messages.
- **Parsing `LastEvent` string into human-friendly labels.** Raw `LastEvent` (e.g., `tool_use:Read`) is not surfaced to users; only aggregated counters are shown. If richer labels are wanted later, a separate pass can add mapping.
- **Changes to `InMemStatusBus` multi-subscriber support.** The existing single-consumer channel is kept; logic is added inside the sole subscriber (`StatusListener`).

## Design

### 1. Architecture Overview

```
worker/pool.go
  handleJob (existing, +1 line):
    … ack, set worker, build accumulator …
    [NEW] Status.Report(jobCtx, accumulator.toReport())  # PID=0 signals prep phase
    executeJob(…)    # OnStarted + reportStatus 5s ticks continue as today

app/bot/status_listener.go (existing, extended)
  StatusListener struct + new fields:
    slack       SlackStatusPoster    # new narrow interface
    lastUpdate  map[string]time.Time # jobID → last Slack update
    mu          sync.Mutex
    clock       func() time.Time     # injectable for tests

  onReport(r):
    store.SetAgentStatus(r)                       # existing
    [NEW] maybeUpdateSlack(r)                      # see §3

app/slack/client.go
  [NEW] UpdateMessageWithButton(channelID, ts, text, actionID, btnText, value) error

app/bot/result_listener.go (existing, +3 lines)
  handleResult terminal write path:
    [NEW] time.AfterFunc(2s, () => slack.UpdateMessage(...text))  # defensive re-write
```

### 2. Worker — Publish Prep StatusReport

**`internal/worker/pool.go`** — in `executeWithTracking` (around line 147), between creating the accumulator and calling `executeJob`, publish one `StatusReport`:

```go
status := &statusAccumulator{
    jobID:    job.ID,
    workerID: wID,
    alive:    true,
}

// existing OnStarted handler …

if err := p.cfg.Queue.Ack(jobCtx, job.ID); err != nil {
    // existing error path
}

p.cfg.Store.SetWorker(job.ID, wID)

// NEW — emit prep-phase signal so StatusListener can surface the worker immediately.
if p.cfg.Status != nil {
    _ = p.cfg.Status.Report(jobCtx, status.toReport())
}

deps := executionDeps{…}
result := executeJob(jobCtx, job, deps, opts, logger)
```

This report has `PID=0`, `AgentCmd=""`, `WorkerID` populated, `Alive=true`. Downstream consumers distinguish prep from running by `PID==0`.

Best-effort: failure to publish is logged by the bus but does not abort the job — the job's prep continues, and the next report (from `OnStarted`) will reach the listener anyway.

### 3. StatusListener Extension

**`internal/bot/status_listener.go`** — extend with Slack dispatch. The existing `Listen` loop body gains a call after `SetAgentStatus`:

```go
type SlackStatusPoster interface {
    UpdateMessageWithButton(channelID, messageTS, text, actionID, buttonText, value string) error
}

type StatusListener struct {
    status queue.StatusBus
    store  queue.JobStore
    slack  SlackStatusPoster    // NEW

    mu          sync.Mutex
    lastUpdate  map[string]time.Time  // jobID → last Slack update time (debounce)
    lastPhase   map[string]string     // jobID → last rendered phase label (force-update on change)

    clock  func() time.Time  // NEW; defaults to time.Now
    logger *slog.Logger
}

func (l *StatusListener) Listen(ctx context.Context) {
    ch, err := l.status.Subscribe(ctx)
    if err != nil { … }
    for {
        select {
        case report, ok := <-ch:
            if !ok { return }
            l.store.SetAgentStatus(report.JobID, report)
            l.maybeUpdateSlack(report)      // NEW
        case <-ctx.Done():
            return
        }
    }
}
```

`maybeUpdateSlack`:

```go
const statusUpdateDebounce = 15 * time.Second

func (l *StatusListener) maybeUpdateSlack(r queue.StatusReport) {
    state, err := l.store.Get(r.JobID)
    if err != nil || state == nil {
        l.logger.Warn("status listener: job state missing", "phase", "失敗", "job_id", r.JobID, "error", err)
        return
    }

    // Terminal state — let ResultListener handle the final message; clean up.
    if isTerminal(state.Status) {
        l.mu.Lock()
        delete(l.lastUpdate, r.JobID)
        l.mu.Unlock()
        return
    }

    if state.StatusMsgTS == "" {
        return // first message not yet posted (defensive; should not happen in practice)
    }

    phase := inferPhase(state, r) // "preparing" | "running"

    l.mu.Lock()
    prevTime, ok := l.lastUpdate[r.JobID]
    prevPhase, _ := l.lastPhase[r.JobID]
    now := l.clock()
    phaseChanged := ok && prevPhase != phase
    debounceExpired := !ok || now.Sub(prevTime) >= statusUpdateDebounce
    if !phaseChanged && !debounceExpired {
        l.mu.Unlock()
        return
    }
    l.lastUpdate[r.JobID] = now
    l.lastPhase[r.JobID] = phase
    l.mu.Unlock()

    text := renderStatusMessage(state, r, phase)
    if text == "" {
        return
    }

    // Second terminal check immediately before the API call — narrows (does not
    // eliminate) the race with ResultListener writing the final message. If we
    // observe the store has since transitioned to terminal, drop our update.
    if latest, err := l.store.Get(r.JobID); err == nil && latest != nil && isTerminal(latest.Status) {
        l.mu.Lock()
        delete(l.lastUpdate, r.JobID)
        delete(l.lastPhase, r.JobID)
        l.mu.Unlock()
        return
    }

    if err := l.slack.UpdateMessageWithButton(
        state.Job.ChannelID, state.StatusMsgTS, text,
        "cancel_job", "取消", r.JobID,
    ); err != nil {
        l.logger.Warn("status 訊息更新失敗", "phase", "失敗", "job_id", r.JobID, "error", err)
        // Non-fatal: next tick will retry.
    }
}

func isTerminal(s queue.JobStatus) bool {
    return s == queue.JobCompleted || s == queue.JobFailed || s == queue.JobCancelled
}

func inferPhase(state *queue.JobState, r queue.StatusReport) string {
    // Prefer store's authoritative status; fall back to PID heuristic only if store
    // hasn't transitioned yet (e.g., very first prep-phase report).
    switch state.Status {
    case queue.JobPreparing:
        return "preparing"
    case queue.JobRunning:
        return "running"
    }
    if r.PID > 0 {
        return "running"
    }
    return "preparing"
}
```

Two maps (`lastUpdate`, `lastPhase`) are maintained under the same mutex. Both cleared on terminal.

### 4. Render Template

```go
func renderStatusMessage(state *queue.JobState, r queue.StatusReport, phase string) string {
    worker := shortWorker(r.WorkerID)

    switch phase {
    case "preparing":
        return fmt.Sprintf(":gear: 準備中 · %s", worker)
    case "running":
        // Defensive: if StartedAt is zero (brief window between JobRunning set
        // and first running-phase report), omit elapsed rather than showing
        // seconds-since-epoch.
        var suffix string
        if !state.StartedAt.IsZero() {
            suffix = fmt.Sprintf(" · 已執行 %s", formatElapsed(time.Since(state.StartedAt)))
        }
        agent := r.AgentCmd
        if agent == "" {
            agent = "agent"
        }
        base := fmt.Sprintf(":hourglass_flowing_sand: 處理中 · %s (%s)%s",
            worker, agent, suffix)
        if r.ToolCalls > 0 || r.FilesRead > 0 {
            base += fmt.Sprintf("\n工具呼叫 %d 次 · 讀檔 %d 份", r.ToolCalls, r.FilesRead)
        }
        return base
    }
    return ""
}

func shortWorker(id string) string {
    if i := strings.LastIndex(id, "/"); i >= 0 {
        return id[i+1:]
    }
    return id
}

func formatElapsed(d time.Duration) string {
    secs := int(d.Seconds())
    return fmt.Sprintf("%dm%02ds", secs/60, secs%60)
}
```

**Design notes:**
- `preparing` template omits agent name and elapsed. Agent is not yet chosen (fallback chain resolves dynamically). `state.StartedAt` is zero until `JobRunning`, so elapsed would be misleading.
- `running` always shows agent name in parentheses — worker assignment + which CLI is running is useful operator/user info.
- Second line only appears when `ToolCalls > 0 || FilesRead > 0`. For non-streaming agents this line is silent.

### 5. Slack Client — `UpdateMessageWithButton`

**`internal/slack/client.go`** — add:

```go
// UpdateMessageWithButton replaces a message's text while preserving a single
// action button (mirrors PostMessageWithButton's block structure).
func (c *Client) UpdateMessageWithButton(
    channelID, messageTS, text, actionID, buttonText, value string,
) error {
    btnBlock := slack.NewActionBlock("cancel_actions",
        slack.NewButtonBlockElement(actionID, value,
            slack.NewTextBlockObject("plain_text", buttonText, false, false)),
    )
    textBlock := slack.NewSectionBlock(
        slack.NewTextBlockObject("mrkdwn", text, false, false), nil, nil)

    start := time.Now()
    _, _, _, err := c.api.UpdateMessage(channelID, messageTS,
        slack.MsgOptionBlocks(textBlock, btnBlock),
    )
    metrics.ExternalDuration.WithLabelValues("slack", "post_message").Observe(time.Since(start).Seconds())
    if err != nil {
        metrics.ExternalErrorsTotal.WithLabelValues("slack", "post_message").Inc()
        return fmt.Errorf("update message with button: %w", err)
    }
    return nil
}
```

The block structure matches `PostMessageWithButton` (`client.go:316`) so Slack recognizes this as an update of the same message layout. `UpdateMessage` (the plain text variant) is left untouched — its existing callers (`result_listener.go:309`, `workflow.go:239/244/318/353/543`) all want to strip blocks.

### 6. Constructor Wiring

**`cmd/agentdock/app.go`** — pass the Slack client into `StatusListener` via its new constructor signature:

```go
// existing:
statusListener := bot.NewStatusListener(bundle.Status, jobStore, logger)

// new:
statusListener := bot.NewStatusListener(bundle.Status, jobStore, slackClient, logger)
```

`*slackclient.Client` already has `UpdateMessageWithButton` after §5, so it satisfies `SlackStatusPoster`.

### 7. ResultListener — Defensive Double-Write

**`internal/bot/result_listener.go`** — where the final status message is written (`result_listener.go:308-310` in `UpdateMessage`-based call), add a second write 2 seconds later:

```go
if job.StatusMsgTS != "" {
    r.slack.UpdateMessage(job.ChannelID, job.StatusMsgTS, text)
    // Defensive re-write: narrows the residual race with StatusListener's
    // in-flight update. Idempotent — same text twice is visually unchanged.
    time.AfterFunc(2*time.Second, func() {
        r.slack.UpdateMessage(job.ChannelID, job.StatusMsgTS, text)
    })
}
```

The timer fires once, no leak. If the app restarts in the 2-second window, the re-write is lost but the first write is already in place — user still sees the final message.

Only applied to terminal updates from `ResultListener`, not to intermediate `UpdateMessage` calls elsewhere (selector flows, cancel flow). Cost: one extra Slack API call per job.

### 8. Logging

Follow `internal/logging/GUIDE.md`:

- Successful Slack update: `logger.Info("status 訊息已更新", "phase", "處理中", "job_id", …, "phase_label", phase, "elapsed", elapsed)` — debug-level if too chatty
- Update failure: `logger.Warn("status 訊息更新失敗", "phase", "失敗", "job_id", …, "error", err)`
- Skip due to missing state: existing warn in `maybeUpdateSlack`

## Error Handling

| Scenario | Behavior |
|---|---|
| `store.Get(jobID)` fails | Log warn, skip this update. Next report retries. |
| `state.Status` is terminal | Skip update, delete from `lastUpdate`/`lastPhase`. ResultListener handles final message. |
| `state.StatusMsgTS == ""` | Defensive skip. Should not happen: workflow posts the status message synchronously at submit (workflow.go:501-504) before any worker picks it up. |
| `UpdateMessageWithButton` returns error (Slack API failure, rate limit, etc.) | Log warn, do NOT retry. Next 15s tick will attempt again. Non-critical path. |
| Worker crashes before emitting final report | `lastUpdate` / `lastPhase` entries leak for that jobID. Mitigation: extend `ResultListener.handleResult` to call `statusListener.ClearJob(jobID)` after processing (tiny helper method). |
| Two `StatusReport`s for the same job arrive concurrently | Mutex serializes `maybeUpdateSlack`. Only one update fires per debounce window. |
| Worker's prep `StatusReport` publish fails | Logged by bus layer. Next report (from `OnStarted`) reaches the listener within 1–30s; user sees `準備中` → `處理中` anyway, just skipping the early-prep update. |
| User clicks cancel during status update | Cancel click hits `cmd/agentdock/app.go` cancel_job handler; it calls `UpdateMessage` (plain text, strips blocks) to show `正在取消...`. A subsequent StatusListener update would overwrite it, so: when status becomes `JobCancelled` (terminal), StatusListener skips — covered by the terminal check. Brief race window (<1s) is acceptable. |
| ResultListener writes final message concurrently with StatusListener in-flight update | `ResultListener` sets `JobCompleted/Failed/Cancelled` on the store (`result_listener.go:303` etc.) BEFORE calling `UpdateMessage`. `StatusListener`'s two-phase terminal check (once at entry, once right before the Slack call) will observe the terminal status in the vast majority of cases and skip. Residual race — both listeners' Slack API calls in flight simultaneously — resolved by Slack's last-write-wins. Worst case: StatusListener's "處理中" overwrites the final message; no further reports arrive, so it stays stuck. Frequency: ≲0.05% of jobs based on observed timings. Mitigation: `ResultListener` issues its final `UpdateMessage` twice — immediately, and again 2 seconds later — a cheap idempotent retry that guarantees the last write is the final text. |

## Testing

### Unit — `internal/bot/status_listener_test.go` (new file)

Stub `SlackStatusPoster` records calls; stub `queue.JobStore` returns configurable states; inject `clock` for deterministic debounce tests.

- `TestMaybeUpdateSlack_PreparingPhase` — PID=0, Status=JobPreparing → calls `UpdateMessageWithButton` with message containing `準備中` and `worker-0`.
- `TestMaybeUpdateSlack_RunningWithToolCalls` — PID>0, ToolCalls=15, FilesRead=8 → two-line message.
- `TestMaybeUpdateSlack_RunningNoToolCalls` — PID>0, ToolCalls=0 (codex case) → one-line message, elapsed visible.
- `TestMaybeUpdateSlack_DebounceSkips` — two reports 5s apart, same phase → only one Slack call.
- `TestMaybeUpdateSlack_PhaseChangeForcesUpdate` — preparing → running within debounce window → both updates fire.
- `TestMaybeUpdateSlack_TerminalSkips` — Status=JobCompleted → no Slack call, `lastUpdate` cleared.
- `TestMaybeUpdateSlack_StoreMissing` — `store.Get` returns error → no Slack call.
- `TestMaybeUpdateSlack_StatusMsgTSEmpty` — state has no StatusMsgTS → no Slack call.
- `TestMaybeUpdateSlack_SlackErrorNonFatal` — stub returns error → no panic, no retry, logs warn.
- `TestShortWorker` — `"host-1/worker-3"` → `"worker-3"`; no slash → input unchanged.
- `TestFormatElapsed` — 0 → `0m00s`; 65s → `1m05s`; 3600s → `60m00s`.
- `TestRenderStatusMessage_Templates` — the three branches (preparing / running / running+stats).

### Unit — `internal/worker/pool_test.go` (extend existing)

- `TestHandleJob_PublishesPrepStatusReport` — stubbed `Status` bus; run handleJob through ack + SetWorker; assert at least one report with `PID=0, WorkerID=…` was published before `executeJob` starts. Existing test helpers already mock Queue/Store.

### Slack client

No new tests. `client_test.go` currently tests only pure functions. `UpdateMessageWithButton` block structure is validated by Slack at runtime (`invalid_blocks` error is a noted landmine in `CLAUDE.md`); wrong structure would fail manual QA immediately.

### Integration / Manual QA (PR checklist)

1. Claude agent job (5 min, multi-repo channel): status message progresses `已加入排隊...` → `準備中 · worker-0` → `處理中 · worker-0 (claude) · Xm Ys` with second line `工具呼叫 N 次 · 讀檔 M 份` updating every 15s.
2. Codex agent job: same progression but second line never appears; elapsed still ticks every 15s.
3. Cancel button present throughout all updates; clicking it mid-prep and mid-run both trigger the usual cancel flow.
4. Very short job (<15s): user sees 1–2 updates max; no spam.
5. Two concurrent jobs from different threads: each thread's status message updates independently.
6. Worker crashes mid-run: final message is produced by ResultListener (via watchdog + failure publish); no orphan status message stuck in "處理中".

## Rollout

Single PR covering:
- `internal/slack/client.go` — new `UpdateMessageWithButton`
- `internal/bot/status_listener.go` — extended struct, new logic, new interface
- `internal/bot/result_listener.go` — defensive double-write on terminal update
- `cmd/agentdock/app.go` — constructor wiring passes `slackClient`
- `internal/worker/pool.go` — one-line prep `Status.Report` call
- `internal/bot/status_listener_test.go` — new unit tests
- `internal/worker/pool_test.go` — one new unit test
- `internal/bot/result_listener_test.go` — extend with double-write assertion

No config flag. Feature is always-on; degrades gracefully per-agent via data-driven template. Existing behavior for terminal messages is unchanged.

No migration. No Redis schema change. `StatusReport` struct unchanged.

Release-please patch bump.
