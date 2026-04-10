# Queue-Based App-Agent Decoupling Design

## Goal

Decouple the app → agent execution path using a producer/consumer queue architecture. The app becomes a pure Slack I/O coordinator (receive triggers, submit jobs, post results), while agent execution moves to independent workers that can run in-process, in separate pods, or on external machines.

## Requirements

- **Priority queue**: Bounded capacity queue with channel-based priority ordering
- **Producer/Consumer abstraction**: Interface designed for remote transport (fully serializable jobs), with in-memory implementation as first transport
- **Result queue**: Workers publish results back; app listens and handles all side effects (GitHub issue creation, Slack posting)
- **Attachment two-phase**: Job carries attachment metadata only; files downloaded after worker Ack to avoid storage bloat
- **Job observability**: Full lifecycle tracking (pending → running → completed/failed) with queue position queryable by Slack users
- **No retry**: Failures reported directly to Slack; existing agent fallback chain handles transient agent errors
- **Side effects in app only**: Workers do not create GitHub issues or post to Slack; they return structured triage results

## Architecture

```
┌─────────────────────────────────────────────────┐
│                  App Pod                         │
│                                                  │
│  Slack Event → Handler (dedup + rate limit)      │
│       ↓                                          │
│  Workflow: Slack UI → thread context → prompt    │
│       ↓                                          │
│  Producer.Submit(job)                            │
│       ↓                                          │
│  ┌──────────────────────────────────────────┐    │
│  │         Transport Layer                   │    │
│  │  ┌──────────────┐  ┌─────────────────┐   │    │
│  │  │ InMemTransport│  │ NATSTransport   │   │    │
│  │  │ (Go channels  │  │ (遠期)          │   │    │
│  │  │ + heap)       │  │                 │   │    │
│  │  └──────────────┘  └─────────────────┘   │    │
│  └──────────┬───────────────────────────────┘    │
│             │                                     │
│  ┌──────────┴──────────┐                         │
│  │ In-Pod Worker Pool  │                         │
│  └─────────────────────┘                         │
│                                                  │
│  ResultListener ← ResultBus                      │
│       ↓                                          │
│  Rejection/Degradation logic                     │
│       ↓                                          │
│  GitHub.CreateIssue() + Slack.PostMessage()       │
└─────────────────────────────────────────────────┘
         │ same JobQueue interface
    ┌────┴──────────┐
    │ External      │
    │ Workers       │
    │ (remote pods, │
    │  local dev    │
    │  machines)    │
    └───────────────┘
```

## Data Structures

### Job (fully JSON-serializable)

```go
type Job struct {
    ID          string            `json:"id"`
    Priority    int               `json:"priority"`
    ChannelID   string            `json:"channel_id"`
    ThreadTS    string            `json:"thread_ts"`
    UserID      string            `json:"user_id"`
    RepoOwner   string            `json:"repo_owner"`
    RepoName    string            `json:"repo_name"`
    Branch      string            `json:"branch"`
    CloneURL    string            `json:"clone_url"`
    Prompt      string            `json:"prompt"`
    RequestID   string            `json:"request_id"`
    Attachments []AttachmentMeta  `json:"attachments"`
    SubmittedAt time.Time         `json:"submitted_at"`
}

type AttachmentMeta struct {
    SlackFileID string `json:"slack_file_id"`
    Filename    string `json:"filename"`
    Size        int64  `json:"size"`
    MimeType    string `json:"mime_type"`
}
```

### JobResult (worker output — no side effects)

```go
type JobResult struct {
    JobID      string    `json:"job_id"`
    Status     string    `json:"status"`         // "completed" | "failed"
    Title      string    `json:"title"`          // issue title
    Body       string    `json:"body"`           // issue markdown body
    Labels     []string  `json:"labels"`         // bug, feature, etc.
    Confidence string    `json:"confidence"`     // high | low
    FilesFound int       `json:"files_found"`
    Questions  int       `json:"open_questions"`
    RawOutput  string    `json:"raw_output"`     // full agent output for debug
    Error      string    `json:"error"`          // error message if failed
    StartedAt  time.Time `json:"started_at"`
    FinishedAt time.Time `json:"finished_at"`
}
```

### Job Lifecycle

```
Submit → Pending (queued, position queryable)
  → Ack → Preparing (downloading attachments + cloning repo)
    → Running (agent executing)
      → Completed / Failed
```

## Interfaces

### Job Queue (transport-agnostic)

```go
type JobQueue interface {
    // Producer side (app)
    Submit(ctx context.Context, job *Job) error
    QueuePosition(jobID string) (int, error)
    QueueDepth() int

    // Consumer side (worker)
    Receive(ctx context.Context) (<-chan *Job, error)
    Ack(ctx context.Context, jobID string) error

    // Worker registration (預留 for external listeners)
    Register(ctx context.Context, info WorkerInfo) error
    Unregister(ctx context.Context, workerID string) error
    ListWorkers(ctx context.Context) ([]WorkerInfo, error)

    Close() error
}

type WorkerInfo struct {
    WorkerID    string   `json:"worker_id"`
    Name        string   `json:"name"`
    Agents      []string `json:"agents"`
    Labels      []string `json:"labels"`
    ConnectedAt time.Time
}
```

### Attachment Store (two-phase download)

```go
type AttachmentStore interface {
    // App side: called after worker Ack, downloads from Slack
    Prepare(ctx context.Context, jobID string, attachments []AttachmentMeta) error

    // Worker side: get attachment access info
    Resolve(ctx context.Context, jobID string) ([]AttachmentReady, error)

    // Cleanup after job completion
    Cleanup(ctx context.Context, jobID string) error
}

type AttachmentReady struct {
    Filename string `json:"filename"`
    URL      string `json:"url"`  // file:// (local) or http:// (remote)
}
```

### Result Bus

```go
type ResultBus interface {
    Publish(ctx context.Context, result *JobResult) error
    Subscribe(ctx context.Context) (<-chan *JobResult, error)
    Close() error
}
```

### Job Store (observability + user queries)

```go
type JobStore interface {
    Get(jobID string) (*JobState, error)
    GetByThread(channelID, threadTS string) (*JobState, error)
    ListPending() ([]*JobState, error)
    Update(jobID string, status JobStatus) error
    ClearDedup(channelID, threadTS string)
}

type JobState struct {
    Job       *Job
    Status    string
    Position  int
    WorkerID  string
    StartedAt time.Time
    WaitTime  time.Duration
}
```

## Priority Queue Implementation

In-memory implementation using `container/heap`:

```go
type priorityQueue []*queueEntry

type queueEntry struct {
    job   *Job
    index int
}

// Higher priority first; FIFO within same priority
func (pq priorityQueue) Less(i, j int) bool {
    if pq[i].job.Priority != pq[j].job.Priority {
        return pq[i].job.Priority > pq[j].job.Priority
    }
    return pq[i].job.SubmittedAt.Before(pq[j].job.SubmittedAt)
}
```

Priority derived from channel config:

```yaml
channel_priority:
  C_INCIDENTS: 100
  C_ONCALL: 80
  default: 50
```

## Worker Pool

```go
type Pool struct {
    queue       JobQueue
    attachments AttachmentStore
    results     ResultBus
    agentRunner *AgentRunner
    repoCache   *RepoCache
    workerCount int
}

func (p *Pool) runWorker(ctx context.Context, id int) {
    jobs, _ := p.queue.Receive(ctx)
    for job := range jobs {
        p.queue.Ack(ctx, job.ID)
        attachments, _ := p.attachments.Resolve(ctx, job.ID)
        repoPath, _ := p.repoCache.Prepare(job.CloneURL, job.Branch)
        copyAttachmentsToRepo(attachments, repoPath)
        output, err := p.agentRunner.Run(ctx, repoPath, job.Prompt)
        result := buildResult(job, output, err)
        p.results.Publish(ctx, result)
    }
}
```

Workers don't need GH_TOKEN — only repo read access (clone). Agent CLI + skills are worker-local.

## Result Listener (App Side)

All side effects handled by app:

```go
func (r *ResultListener) Listen(ctx context.Context) {
    ch, _ := r.results.Subscribe(ctx)
    for result := range ch {
        job, _ := r.jobStore.Get(result.JobID)
        switch {
        case result.Status == "failed":
            r.slack.PostMessage(..., formatError(result))
        case result.Confidence == "low":
            r.slack.PostMessage(..., "判斷不屬於此 repo，已跳過")
        case result.FilesFound == 0 || result.Questions >= 5:
            url, _ := r.github.CreateIssue(owner, repo, result.Title,
                stripTriageSection(result.Body), result.Labels)
            r.slack.PostMessage(..., url)
        default:
            url, _ := r.github.CreateIssue(owner, repo, result.Title,
                result.Body, result.Labels)
            r.slack.PostMessage(..., url)
        }
        r.attachments.Cleanup(ctx, result.JobID)
        r.jobStore.ClearDedup(job.ChannelID, job.ThreadTS)
    }
}
```

## Handler Changes

```go
func (h *Handler) HandleTrigger(event TriggerEvent) bool {
    // dedup + rate limit unchanged
    if h.threadDedup.isDuplicate(...) { return false }
    if !h.userLimit.allow(...)       { return false }
    if !h.channelLimit.allow(...)    { return false }

    // Submit to queue instead of semaphore
    job := buildJob(event)
    err := h.queue.Submit(ctx, job)
    if err == ErrQueueFull {
        h.slack.PostMessage(..., "系統忙碌，請稍後再試")
        return false
    }

    // Immediate feedback
    pos, _ := h.queue.QueuePosition(job.ID)
    if pos == 0 {
        h.slack.PostMessage(..., "正在處理你的請求...")
    } else {
        h.slack.PostMessage(..., fmt.Sprintf("已加入排隊，前面有 %d 個請求", pos))
    }
    return true
}
```

## Config

```yaml
queue:
  capacity: 50
  transport: inmem         # inmem | nats | redis (遠期)

channel_priority:
  C_INCIDENTS: 100
  C_ONCALL: 80
  default: 50

workers:
  count: 3

attachments:
  store: local             # local | s3 (遠期)
  temp_dir: /tmp/triage-attachments
  ttl: 30m

# Existing config retained
rate_limit:
  per_user: 5
  per_channel: 20
  window: 1m
```

## File Structure (new/changed)

```
internal/
  queue/
    interface.go        # JobQueue, ResultBus, AttachmentStore, JobStore
    job.go              # Job, JobResult, JobState, AttachmentMeta
    priority.go         # container/heap priority queue implementation
    inmem.go            # InMemTransport (implements all interfaces)
  worker/
    pool.go             # Worker pool startup and management
    executor.go         # Single job execution (clone, agent, parse)
  bot/
    handler.go          # CHANGED: semaphore → queue.Submit()
    workflow.go         # CHANGED: split into submit-side and worker-side
    prompt.go           # CHANGED: no longer instruct agent to create issues
    parser.go           # CHANGED: parse structured triage output instead of issue URL
  config/
    config.go           # CHANGED: add queue, channel_priority, workers, attachments config
```

## What's Deferred (遠期)

- External transport implementations (NATS, Redis Stream, SQS)
- Worker authentication and registration enforcement
- Job routing / filtering (by repo, labels)
- S3 attachment store
- ResultCallback interface (webhook-based result delivery)
- Dynamic worker scaling

## Migration Notes

- `max_concurrent` config becomes `workers.count` (backward-compatible default)
- Prompt changes: agent no longer runs `gh issue create`; outputs structured triage result
- Parser changes: new output format for title/body/labels/metadata instead of `===TRIAGE_RESULT===`
- Semaphore removal from handler.go
- Existing tests need updating for new flow split
