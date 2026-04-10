# Agent Process Tracking & Kill Design

## Goal

Add real-time agent process visibility and kill capability across all deployment modes (in-memory, remote worker, external listener). Enable operators and users to see what a running agent is doing, detect stuck agents, and terminate wasteful sessions.

## Requirements

- **Process tracking**: Track PID, command, alive status for all agents; track stream events (tool calls, files read, output bytes) for agents that support stream-json
- **Kill mechanism**: Four trigger sources — manual HTTP, watchdog timeout, agent idle detection, Slack user cancel — all unified through a single CommandBus
- **Three deployment modes**: In-memory (direct), remote worker (via transport), external listener (via transport) — same interfaces, different transport implementations
- **Zero agent slowdown**: Stream parsing runs in a separate goroutine reading stdout pipe; agent writes at its own pace
- **Graceful termination**: SIGTERM first, SIGKILL after grace period

## Architecture

```
┌────────────────────────────────────────────────────────┐
│                       App Side                          │
│                                                         │
│  Kill triggers:                                         │
│    DELETE /jobs/{id}     ─┐                             │
│    Watchdog timeout      ─┤→ CommandBus.Send(kill)      │
│    Idle detection        ─┤                             │
│    Slack user cancel     ─┘                             │
│                                                         │
│  StatusListener ← StatusBus.Subscribe()                 │
│    → updates JobStore with AgentStatus                  │
│    → idle detection checks last_event_at                │
│                                                         │
│  GET /jobs ← reads JobStore (includes agent status)     │
└──────────────┬─────────────────────┬───────────────────┘
               │ CommandBus          │ StatusBus
               ↓                     ↑
┌──────────────┴─────────────────────┴───────────────────┐
│                     Worker Side                         │
│                                                         │
│  CommandListener ← CommandBus.Receive()                 │
│    → ProcessRegistry.Kill(jobID)                        │
│                                                         │
│  Worker goroutine:                                      │
│    cmd.StdoutPipe() → streamReader goroutine            │
│      → parse events (stream-json) or just count bytes   │
│      → every status_interval: StatusBus.Report(status)  │
│    cmd.Start() → ProcessRegistry.Register(jobID, proc)  │
│    cmd.Wait()  → ProcessRegistry.Remove(jobID)          │
└────────────────────────────────────────────────────────┘
```

## New Interfaces

### CommandBus

```go
type Command struct {
    JobID  string `json:"job_id"`
    Action string `json:"action"` // "kill"
}

type CommandBus interface {
    Send(ctx context.Context, cmd Command) error
    Receive(ctx context.Context) (<-chan Command, error)
    Close() error
}
```

### StatusBus

```go
type StatusReport struct {
    JobID       string    `json:"job_id"`
    WorkerID    string    `json:"worker_id"`
    PID         int       `json:"pid"`
    AgentCmd    string    `json:"agent_cmd"`
    Alive       bool      `json:"alive"`
    LastEvent   string    `json:"last_event,omitempty"`
    LastEventAt time.Time `json:"last_event_at"`
    ToolCalls   int       `json:"tool_calls"`
    FilesRead   int       `json:"files_read"`
    OutputBytes int       `json:"output_bytes"`
}

type StatusBus interface {
    Report(ctx context.Context, report StatusReport) error
    Subscribe(ctx context.Context) (<-chan StatusReport, error)
    Close() error
}
```

### InMemTransport Extension

`InMemTransport` gains two more channels:

```go
type InMemTransport struct {
    // ...existing fields (jobCh, resultCh, pq, etc.)

    commandCh chan Command       // buffer: 10 (kill commands are rare)
    statusCh  chan StatusReport  // buffer: workerCount * 2 (batched reports)
}
```

In-memory implementation is trivial — `Send` writes to `commandCh`, `Receive` returns `commandCh`. Same for StatusBus. No new struct needed; extend `InMemTransport` to implement both.

Channel buffer sizes: `commandCh` buffered at 10 (kill commands are infrequent), `statusCh` buffered at `workerCount * 2` (each worker reports every `status_interval`, need enough buffer to avoid blocking workers).

For future remote transport (NATS/Redis), CommandBus maps to a pub/sub topic per worker (or broadcast), StatusBus maps to another topic.

## ProcessRegistry (Worker-Side)

Lives on the worker side. Each worker process has one. Holds references to running OS processes for kill.

```go
type ProcessRegistry struct {
    mu        sync.RWMutex
    processes map[string]*runningAgent // jobID → agent
}

type runningAgent struct {
    JobID     string
    PID       int
    Command   string
    Process   *os.Process
    StartedAt time.Time
    Cancel    context.CancelFunc // cancel the job's context
}

func (r *ProcessRegistry) Register(jobID string, proc *os.Process, command string, cancel context.CancelFunc)
func (r *ProcessRegistry) Kill(jobID string) error   // SIGTERM → wait 3s → SIGKILL
func (r *ProcessRegistry) Get(jobID string) (*runningAgent, bool)
func (r *ProcessRegistry) Remove(jobID string)
func (r *ProcessRegistry) List() []runningAgent
```

### Kill implementation

Each `runningAgent` has a `done` channel that `Remove` closes. This avoids polling and race conditions.

```go
type runningAgent struct {
    JobID     string
    PID       int
    Command   string
    Process   *os.Process
    StartedAt time.Time
    Cancel    context.CancelFunc
    done      chan struct{} // closed by Remove()
}

func (r *ProcessRegistry) Register(jobID string, proc *os.Process, command string, cancel context.CancelFunc) {
    r.mu.Lock()
    defer r.mu.Unlock()
    r.processes[jobID] = &runningAgent{
        JobID:     jobID,
        PID:       proc.Pid,
        Command:   command,
        Process:   proc,
        StartedAt: time.Now(),
        Cancel:    cancel,
        done:      make(chan struct{}),
    }
}

func (r *ProcessRegistry) Remove(jobID string) {
    r.mu.Lock()
    agent, ok := r.processes[jobID]
    if ok {
        delete(r.processes, jobID)
        close(agent.done) // signal Kill() if waiting
    }
    r.mu.Unlock()
}

func (r *ProcessRegistry) Kill(jobID string) error {
    r.mu.RLock()
    agent, ok := r.processes[jobID]
    r.mu.RUnlock()
    if !ok {
        return fmt.Errorf("no running agent for job %q", jobID)
    }

    // Copy references under lock — safe to use after unlock.
    proc := agent.Process
    cancel := agent.Cancel
    done := agent.done

    // 1. Try graceful: SIGTERM
    proc.Signal(syscall.SIGTERM)

    // 2. Wait for Remove() to close done, or timeout
    select {
    case <-done:
        return nil // graceful exit confirmed
    case <-time.After(3 * time.Second):
        // 3. Force kill
        proc.Kill()
        cancel()
        return nil
    }
}
```

## Agent Runner Changes

### Config addition

```go
type AgentConfig struct {
    Command  string        `yaml:"command"`
    Args     []string      `yaml:"args"`
    Timeout  time.Duration `yaml:"timeout"`
    SkillDir string        `yaml:"skill_dir"`
    Stream   bool          `yaml:"stream"`   // NEW: enable stream-json event tracking
}
```

### runOne refactor

Change from `cmd.Start()` + `strings.Builder` to `cmd.StdoutPipe()` + goroutine reader:

```go
func (r *AgentRunner) runOne(ctx, logger, agent, workDir, prompt) (string, error) {
    cmd := exec.CommandContext(ctx, agent.Command, args...)

    stdoutPipe, _ := cmd.StdoutPipe()
    var stderr strings.Builder
    cmd.Stderr = &stderr

    cmd.Start()

    // Notify: PID + command (worker stores in JobStore)
    if r.onStarted != nil {
        r.onStarted(cmd.Process.Pid, agent.Command)
    }
    // Worker-internal: capture process handle for ProcessRegistry
    if r.onProcess != nil {
        r.onProcess(cmd.Process)
    }

    // Read stdout in goroutine — collects full output + optionally parses stream events.
    // Uses WaitGroup to ensure reader finishes BEFORE cmd.Wait() closes the pipe.
    var fullOutput strings.Builder
    eventCh := make(chan StreamEvent, 1000) // large buffer, drop on full
    var readerWg sync.WaitGroup
    readerWg.Add(1)

    go func() {
        defer readerWg.Done()
        defer close(eventCh)
        scanner := bufio.NewScanner(stdoutPipe)
        scanner.Buffer(make([]byte, 1024*1024), 1024*1024) // 1MB line buffer
        for scanner.Scan() {
            line := scanner.Text()
            fullOutput.WriteString(line)
            fullOutput.WriteByte('\n')

            if agent.Stream {
                if event, ok := parseStreamEvent(line); ok {
                    select {
                    case eventCh <- event:
                    default: // drop if channel full — non-blocking
                    }
                }
            }
        }
    }()

    // Forward events to callback with context awareness
    if r.onEvent != nil {
        go func() {
            for {
                select {
                case event, ok := <-eventCh:
                    if !ok {
                        return
                    }
                    r.onEvent(event)
                case <-ctx.Done():
                    return
                }
            }
        }()
    }

    // Wait for reader to finish BEFORE cmd.Wait closes the pipe.
    readerWg.Wait()

    err := cmd.Wait()
    // ... error handling same as before

    return fullOutput.String(), nil
}
```

### Stream Event Types

```go
type StreamEvent struct {
    Type      string // "tool_use", "tool_result", "message_delta", "result"
    ToolName  string // e.g. "Read", "Bash", "Grep" (for tool_use)
    TextBytes int    // bytes of text generated (for message_delta)
}

func parseStreamEvent(line string) (StreamEvent, bool) {
    // Parse NDJSON line from claude --output-format stream-json
    var raw map[string]any
    if json.Unmarshal([]byte(line), &raw) != nil {
        return StreamEvent{}, false
    }
    eventType, _ := raw["type"].(string)
    switch eventType {
    case "tool_use":
        name, _ := raw["name"].(string)
        return StreamEvent{Type: "tool_use", ToolName: name}, true
    case "message_delta":
        // Count text bytes in delta
        if delta, ok := raw["delta"].(map[string]any); ok {
            if text, ok := delta["text"].(string); ok {
                return StreamEvent{Type: "message_delta", TextBytes: len(text)}, true
            }
        }
        return StreamEvent{Type: "message_delta"}, true
    default:
        return StreamEvent{}, false
    }
}
```

### AgentRunner callbacks

The `onStarted` callback does NOT pass `*os.Process` directly — that leaks OS concerns into the callback chain. Instead, the worker wraps it internally:

```go
type AgentRunner struct {
    agents      []config.AgentConfig
    githubToken string
    onStarted   func(pid int, command string)    // notifies worker of PID/command
    onProcess   func(proc *os.Process)           // worker-internal: captures process handle
    onEvent     func(event StreamEvent)           // stream events
}
```

The `TrackedRunner` interface is updated:

```go
type TrackedRunner interface {
    Runner
    SetOnStarted(fn func(pid int, command string))
    SetOnProcess(fn func(proc *os.Process))
    SetOnEvent(fn func(event StreamEvent))
}
```

## Worker Integration

### Command Listener (dedicated goroutine)

Kill commands MUST be processed while jobs are executing. A dedicated command listener goroutine runs independently of the job execution loop:

```go
func (p *Pool) Start(ctx context.Context) {
    // Dedicated command listener — NOT inside worker loop
    go p.commandListener(ctx)

    for i := 0; i < p.cfg.WorkerCount; i++ {
        go p.runWorker(ctx, i)
    }
}

func (p *Pool) commandListener(ctx context.Context) {
    commands, _ := p.cfg.Commands.Receive(ctx)
    for {
        select {
        case cmd, ok := <-commands:
            if !ok {
                return
            }
            if cmd.Action == "kill" {
                if err := p.registry.Kill(cmd.JobID); err != nil {
                    slog.Warn("kill command failed", "job_id", cmd.JobID, "error", err)
                }
            }
        case <-ctx.Done():
            return
        }
    }
}

func (p *Pool) runWorker(ctx context.Context, id int) {
    jobs, _ := p.cfg.Queue.Receive(ctx)
    for {
        select {
        case job, ok := <-jobs:
            if !ok {
                return
            }
            p.executeWithTracking(ctx, id, job)
        case <-ctx.Done():
            return
        }
    }
}

func (p *Pool) executeWithTracking(ctx context.Context, workerID int, job *queue.Job) {
    // Per-job context with cancel for kill support
    jobCtx, jobCancel := context.WithCancel(ctx)
    defer jobCancel()

    // Status accumulator — collects stream events, batches reports
    status := &statusAccumulator{
        jobID:    job.ID,
        workerID: fmt.Sprintf("worker-%d", workerID),
    }

    // Wire up callbacks before execution
    // (AgentRunner callbacks are set per-job, called from the runner goroutine)
    // ... setup onStarted to register in ProcessRegistry with jobCancel
    // ... setup onEvent to feed status accumulator

    // Start periodic status reporting
    stopReporter := make(chan struct{})
    go p.reportStatus(jobCtx, status, stopReporter)

    // Execute job (blocks until agent finishes)
    result := executeJob(jobCtx, job, deps)

    close(stopReporter)
    p.registry.Remove(job.ID)

    // Final status report
    status.alive = false
    p.cfg.Status.Report(ctx, status.toReport())

    p.cfg.Results.Publish(ctx, result)
}

func (p *Pool) reportStatus(ctx context.Context, status *statusAccumulator, stop <-chan struct{}) {
    ticker := time.NewTicker(p.cfg.StatusInterval)
    defer ticker.Stop()
    for {
        select {
        case <-ticker.C:
            p.cfg.Status.Report(ctx, status.toReport())
        case <-stop:
            return
        case <-ctx.Done():
            return
        }
    }
}
```

### statusAccumulator

```go
type statusAccumulator struct {
    mu          sync.Mutex
    jobID       string
    workerID    string
    pid         int
    agentCmd    string
    alive       bool
    lastEvent   string
    lastEventAt time.Time
    toolCalls   int
    filesRead   int
    outputBytes int
}

func (s *statusAccumulator) recordEvent(event StreamEvent) {
    s.mu.Lock()
    defer s.mu.Unlock()
    s.lastEventAt = time.Now()
    switch event.Type {
    case "tool_use":
        s.toolCalls++
        s.lastEvent = "tool_use:" + event.ToolName
        if event.ToolName == "Read" {
            s.filesRead++
        }
    case "message_delta":
        s.outputBytes += event.TextBytes
        s.lastEvent = "message_delta"
    }
}

func (s *statusAccumulator) toReport() StatusReport {
    s.mu.Lock()
    defer s.mu.Unlock()
    return StatusReport{
        JobID:       s.jobID,
        WorkerID:    s.workerID,
        PID:         s.pid,
        AgentCmd:    s.agentCmd,
        Alive:       s.alive,
        LastEvent:   s.lastEvent,
        LastEventAt: s.lastEventAt,
        ToolCalls:   s.toolCalls,
        FilesRead:   s.filesRead,
        OutputBytes: s.outputBytes,
    }
}
```

## App-Side: StatusListener + Idle Detection

### StatusListener

Runs as a goroutine on the app side. Receives status reports and updates the JobStore.

```go
type StatusListener struct {
    status   StatusBus
    store    JobStore
    commands CommandBus
    idleTTL  time.Duration // agent_idle_timeout from config
}

func (l *StatusListener) Listen(ctx context.Context) {
    ch, _ := l.status.Subscribe(ctx)
    for {
        select {
        case report, ok := <-ch:
            if !ok {
                return // channel closed
            }
            // SetAgentStatus is the single authoritative update — replaces SetAgent.
            // SetAgent (PID + command only) is removed; SetAgentStatus covers all fields.
            l.store.SetAgentStatus(report.JobID, report)
        case <-ctx.Done():
            return
        }
    }
}
```

### Idle Detection

Integrated into the existing Watchdog. Check both job-level timeout AND agent idle timeout:

```go
func (w *Watchdog) check() {
    all, _ := w.store.ListAll()
    now := time.Now()

    for _, state := range all {
        if state.Status == JobCompleted || state.Status == JobFailed {
            continue
        }

        // 1. Job-level timeout (existing)
        if now.Sub(state.Job.SubmittedAt) > w.jobTimeout {
            w.killAndNotify(state, "job timeout")
            continue
        }

        // 2. Agent idle timeout (new, stream-json only)
        if w.idleTimeout > 0 && !state.AgentLastEventAt.IsZero() {
            if now.Sub(state.AgentLastEventAt) > w.idleTimeout {
                w.killAndNotify(state, "agent idle timeout")
                continue
            }
        }
    }
}

func (w *Watchdog) killAndNotify(state *JobState, reason string) {
    w.commands.Send(context.Background(), Command{JobID: state.Job.ID, Action: "kill"})
    w.store.UpdateStatus(state.Job.ID, JobFailed)
    if w.notifier != nil {
        w.notifier(state.Job, state.Status, reason)
    }
}
```

### Updated StuckNotifier signature

The existing `StuckNotifier` changes from `func(job *Job, status JobStatus, stuckDuration time.Duration)` to:

```go
type StuckNotifier func(job *Job, status JobStatus, reason string)
```

This supports both timeout reasons ("job timeout") and idle reasons ("agent idle timeout"). The existing `FormatStuckMessage` helper and the main.go watchdog setup must be updated accordingly:

```go
func FormatStuckMessage(job *Job, status JobStatus, reason string) string {
    return fmt.Sprintf(":warning: Job 已終止 (%s)，狀態停在 `%s`，repo: `%s`。請重新觸發。",
        reason, status, job.Repo)
}
```
```

## HTTP Endpoints

### GET /jobs (enhanced)

Returns agent tracking info when available:

```json
{
  "queue_depth": 0,
  "total": 1,
  "jobs": [
    {
      "id": "req-abc123",
      "status": "running",
      "repo": "org/backend",
      "age": "2m30s",
      "agent": {
        "pid": 12345,
        "command": "claude",
        "alive": true,
        "last_event": "tool_use:Read",
        "last_event_age": "3s",
        "tool_calls": 12,
        "files_read": 8,
        "output_bytes": 15360
      }
    }
  ]
}
```

### DELETE /jobs/{id}

Kill a running job:

```
DELETE /jobs/req-abc123

Response 200: {"status": "killed", "job_id": "req-abc123"}
Response 404: {"error": "job not found"}
Response 409: {"error": "job not running"}
```

Flow:
1. Validate job exists and is in running/preparing/pending state
2. Mark job as `JobFailed` in JobStore with reason "cancelled"
3. `CommandBus.Send({job_id, action: "kill"})` — worker kills process if running
4. If pending (still in queue): worker will dequeue the job but skip execution because JobStore shows `JobFailed`. No need for a `Cancel` method on JobQueue — the worker checks status after dequeue.
5. Return immediately (async — result comes via ResultListener or worker skip)

Note: We do NOT add a `Remove/Cancel` method to `JobQueue` interface. Instead, workers check `JobStore.Get(jobID).Status` after dequeuing — if already `JobFailed`, they skip execution and publish a cancelled result. This keeps the interface simple.

## Slack User Cancel

User types ONLY a cancel keyword (exact match, trimmed) in a thread that has an active job. This avoids false positives from messages like "don't cancel the meeting":

```go
// isCancel requires the entire message (trimmed) to be a cancel keyword.
func isCancel(text string) bool {
    text = strings.TrimSpace(strings.ToLower(text))
    switch text {
    case "取消", "cancel", "stop", "abort":
        return true
    }
    return false
}

// In the Socket Mode event loop
case *slackevents.MessageEvent:
    if isCancel(inner.Text) && inner.ThreadTimeStamp != "" {
        state, err := jobStore.GetByThread(inner.Channel, inner.ThreadTimeStamp)
        if err == nil && isActive(state.Status) {
            commandBus.Send(ctx, Command{JobID: state.Job.ID, Action: "kill"})
            slackClient.PostMessage(inner.Channel,
                ":stop_sign: 正在取消...", inner.ThreadTimeStamp)
        }
    }
```

## Config

```yaml
queue:
  capacity: 50
  transport: inmem
  job_timeout: 20m              # watchdog: max job lifetime
  agent_idle_timeout: 5m        # stream-json agent: no events for this long = stuck
  status_interval: 5s           # worker status report frequency
  kill_grace_period: 3s         # SIGTERM → SIGKILL wait time

agents:
  claude:
    command: claude
    args: ["--print", "--output-format", "stream-json", "-p", "{prompt}"]
    timeout: 15m
    skill_dir: ".claude/skills"
    stream: true
  opencode:
    command: opencode
    args: ["--prompt", "{prompt}"]
    timeout: 5m
    skill_dir: ".opencode/skills"
    stream: false
```

## JobStore Extension

```go
type JobState struct {
    // ...existing fields
    // Remove: AgentPID, AgentCommand (replaced by full status below)
    AgentStatus *StatusReport // nil if no status reported yet
}

type JobStore interface {
    // ...existing methods
    // Remove: SetAgent(jobID string, pid int, command string) — replaced by SetAgentStatus
    SetAgentStatus(jobID string, report StatusReport) error
}
```

Note: The existing `SetAgent` method is removed. `SetAgentStatus` is the single write path for all agent tracking data. Called by `StatusListener` when it receives periodic reports from workers. The first report (from `onStarted`) contains PID and command; subsequent reports add event data.

## File Structure (new/changed)

```
internal/
  queue/
    interface.go          # CHANGED: add CommandBus, StatusBus, StatusReport, Command
    job.go                # CHANGED: extend JobState with agent tracking fields
    inmem.go              # CHANGED: add commandCh, statusCh
    memstore.go           # CHANGED: add SetAgentStatus, extend ListAll
    httpstatus.go         # CHANGED: enhanced /jobs response, add DELETE /jobs/{id}
    watchdog.go           # CHANGED: add idle detection, use CommandBus for kill
    registry.go           # NEW: ProcessRegistry (worker-side)
    stream.go             # NEW: StreamEvent, parseStreamEvent
  worker/
    pool.go               # CHANGED: command listener, per-job context, status reporting
    executor.go           # CHANGED: wire onStarted/onEvent callbacks
    status.go             # NEW: statusAccumulator
  bot/
    agent.go              # CHANGED: StdoutPipe reader, onEvent callback, Stream support
  config/
    config.go             # CHANGED: add Stream, agent_idle_timeout, status_interval, kill_grace_period
cmd/
  bot/
    main.go               # CHANGED: wire StatusListener, CommandBus, updated watchdog
```

## Deployment Mode Summary

| Capability | In-Memory | Remote Worker | External Listener |
|-----------|-----------|---------------|-------------------|
| Kill | CommandBus (channel) → ProcessRegistry.Kill() | CommandBus (NATS/Redis) → Worker's ProcessRegistry.Kill() | Same as remote |
| Status tracking | StatusBus (channel) → JobStore | StatusBus (NATS/Redis) → JobStore | Same as remote |
| Stream events | Same-process, zero latency | Batched every status_interval | Same as remote |
| PID alive check | Direct (same machine) | Worker reports alive flag | Same as remote |
| Idle detection | App-side Watchdog reads JobStore | Same — based on StatusReport timestamps | Same |

All modes use the same interfaces. Only the transport layer differs.

## Migration Notes

- `agent.Args` for claude should change from `["--print", "-p", "{prompt}"]` to `["--print", "--output-format", "stream-json", "-p", "{prompt}"]`
- Add `stream: true` to claude agent config
- Add `agent_idle_timeout`, `status_interval`, `kill_grace_period` to queue config
- AgentRunner `onStarted` callback signature changes: adds `*os.Process` parameter
- Worker pool gains CommandBus dependency
- Existing tests need updating for new callback signatures
