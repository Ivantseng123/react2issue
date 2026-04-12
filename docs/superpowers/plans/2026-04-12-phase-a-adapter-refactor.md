# Phase A: Adapter Abstraction + Bundle Refactor

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Refactor the architecture to introduce Adapter abstraction, Coordinator routing, and common Bundle type — without changing any runtime behavior. Prepares for Phase B (Redis transport).

**Architecture:** Coordinator implements JobQueue (Decorator pattern) and routes jobs by TaskType. LocalAdapter wraps worker.Pool and owns its lifecycle. Bundle becomes a common struct with interface-typed fields. Workflow code is untouched — it continues calling `w.queue.Submit()`.

**Tech Stack:** Go, existing internal packages only (no new dependencies)

**Circular dependency note:** `internal/worker` imports `internal/queue`, so `internal/queue` cannot import `internal/worker`. The `Adapter` interface + `AdapterDeps` live in `internal/queue` (no worker dependency). `LocalAdapter` + `LocalAdapterConfig` live in `cmd/bot/` (imports both packages, following the existing adapter pattern in main.go).

---

## File Structure

### New Files
| File | Responsibility |
|------|---------------|
| `internal/queue/bundle.go` | Common `Bundle` struct with interface-typed fields + `Close()` |
| `internal/queue/coordinator.go` | `Coordinator` implementing `JobQueue` interface, routes `Submit` by `TaskType` |
| `internal/queue/coordinator_test.go` | Tests for Coordinator routing + delegation |
| `internal/queue/adapter.go` | `Adapter` interface + `AdapterDeps` (no worker import) |
| `cmd/bot/local_adapter.go` | `LocalAdapter` + `LocalAdapterConfig` (imports both queue and worker) |

### Modified Files
| File | Change |
|------|--------|
| `internal/queue/job.go` | Add `TaskType` field to `Job` |
| `internal/queue/inmem_bundle.go` | Return `*Bundle` instead of `*InMemBundle`, remove `InMemBundle` struct |
| `cmd/bot/main.go` | Wire Coordinator + LocalAdapter instead of direct bundle.Queue + worker.Pool |

### Unchanged Files
All files in `internal/bot/`, `internal/worker/`, `internal/slack/`, `internal/github/`, `internal/config/`.

---

### Task 0: Commit existing bug fixes

**Files:**
- All currently modified files (git status shows changes)

- [ ] **Step 1: Stage and commit all bug fixes**

```bash
git add internal/bot/result_listener.go internal/bot/result_listener_test.go \
       internal/bot/workflow.go internal/queue/job.go internal/queue/stream.go \
       internal/queue/stream_test.go internal/worker/executor.go \
       internal/worker/pool.go cmd/bot/main.go
git commit -m "fix: attachment resolution, stream parser, status updates, skill mount

- Skip attachments.Resolve when job has no attachments (unblocks worker)
- Wire Prepare() in workflow after Submit()
- Fix stream-json parser for Claude CLI format (assistant events)
- Fix skill mount path to {skill_dir}/{name}/SKILL.md
- Mount skills to all agent skill dirs (fallback chain)
- Send final status report after job completes (captures cost)
- Update original status message instead of posting new one
- Add StatusMsgTS to Job for message tracking"
```

- [ ] **Step 2: Verify clean state**

Run: `git status`
Expected: only untracked files (docs/)

---

### Task 1: Add TaskType to Job

**Files:**
- Modify: `internal/queue/job.go:18-34`

- [ ] **Step 1: Add TaskType field**

In `internal/queue/job.go`, add `TaskType` to the `Job` struct after `StatusMsgTS`:

```go
type Job struct {
	ID          string            `json:"id"`
	Priority    int               `json:"priority"`
	Seq         uint64            `json:"seq"`
	ChannelID   string            `json:"channel_id"`
	ThreadTS    string            `json:"thread_ts"`
	UserID      string            `json:"user_id"`
	Repo        string            `json:"repo"`
	Branch      string            `json:"branch"`
	CloneURL    string            `json:"clone_url"`
	Prompt      string            `json:"prompt"`
	Skills      map[string]string `json:"skills"`
	RequestID   string            `json:"request_id"`
	Attachments []AttachmentMeta  `json:"attachments"`
	StatusMsgTS string            `json:"status_msg_ts,omitempty"`
	TaskType    string            `json:"task_type,omitempty"`
	SubmittedAt time.Time         `json:"submitted_at"`
}
```

- [ ] **Step 2: Run tests**

Run: `go test ./...`
Expected: all 101 tests pass (field is additive, zero value is empty string)

- [ ] **Step 3: Commit**

```bash
git add internal/queue/job.go
git commit -m "feat: add TaskType field to Job for capability routing"
```

---

### Task 2: Create common Bundle struct

**Files:**
- Create: `internal/queue/bundle.go`

- [ ] **Step 1: Create bundle.go**

```go
package queue

// Bundle holds the five transport interfaces. Both inmem and redis
// constructors return this type, so all downstream code is transport-agnostic.
type Bundle struct {
	Queue       JobQueue
	Results     ResultBus
	Status      StatusBus
	Commands    CommandBus
	Attachments AttachmentStore
}

type closer interface {
	Close() error
}

func (b *Bundle) Close() error {
	for _, c := range []any{b.Queue, b.Results, b.Commands, b.Status} {
		if cl, ok := c.(closer); ok {
			cl.Close()
		}
	}
	return nil
}
```

- [ ] **Step 2: Verify build**

Run: `go build ./...`
Expected: compiles (no consumers yet)

- [ ] **Step 3: Commit**

```bash
git add internal/queue/bundle.go
git commit -m "feat: add common Bundle struct with interface-typed fields"
```

---

### Task 3: Refactor InMemBundle to return Bundle

**Files:**
- Modify: `internal/queue/inmem_bundle.go`

- [ ] **Step 1: Replace InMemBundle struct with NewInMemBundle returning *Bundle**

Replace the entire content of `internal/queue/inmem_bundle.go`:

```go
package queue

func NewInMemBundle(capacity int, workerCount int, store JobStore) *Bundle {
	return &Bundle{
		Queue:       NewInMemJobQueue(capacity, store),
		Results:     NewInMemResultBus(capacity),
		Attachments: NewInMemAttachmentStore(),
		Commands:    NewInMemCommandBus(10),
		Status:      NewInMemStatusBus(workerCount * 2),
	}
}
```

The `Close()` method is now on `Bundle` (from bundle.go). The `InMemBundle` struct is removed.

- [ ] **Step 2: Run tests**

Run: `go test ./...`
Expected: all 101 tests pass. Tests access `bundle.Queue`, `bundle.Results`, etc. which are now interface types — all callers already use them through interface methods.

- [ ] **Step 3: Verify integration tests specifically**

Run: `go test ./internal/queue/ -run TestFullFlow -v`
Expected: both `TestFullFlow_SubmitToResult` and `TestFullFlow_PriorityOrdering` pass.

Run: `go test ./internal/worker/ -v`
Expected: both pool tests pass.

- [ ] **Step 4: Commit**

```bash
git add internal/queue/inmem_bundle.go
git commit -m "refactor: InMemBundle returns common Bundle with interface fields"
```

---

### Task 4: Create Coordinator (JobQueue decorator)

**Files:**
- Create: `internal/queue/coordinator.go`
- Create: `internal/queue/coordinator_test.go`

- [ ] **Step 1: Write coordinator tests**

Create `internal/queue/coordinator_test.go`:

```go
package queue

import (
	"context"
	"testing"
)

func TestCoordinator_RoutesToRegisteredQueue(t *testing.T) {
	store := NewMemJobStore()
	bundleA := NewInMemBundle(10, 1, store)
	defer bundleA.Close()
	bundleB := NewInMemBundle(10, 1, store)
	defer bundleB.Close()

	coord := NewCoordinator(bundleA.Queue)
	coord.RegisterQueue("triage", bundleA.Queue)
	coord.RegisterQueue("review", bundleB.Queue)

	ctx := context.Background()

	// Submit triage job.
	err := coord.Submit(ctx, &Job{ID: "j1", TaskType: "triage", Priority: 50})
	if err != nil {
		t.Fatal(err)
	}

	// Submit review job.
	err = coord.Submit(ctx, &Job{ID: "j2", TaskType: "review", Priority: 50})
	if err != nil {
		t.Fatal(err)
	}

	// Triage job should be in bundleA's queue.
	chA, _ := bundleA.Queue.Receive(ctx)
	select {
	case job := <-chA:
		if job.ID != "j1" {
			t.Errorf("bundleA got job %q, want j1", job.ID)
		}
	default:
		t.Error("bundleA queue empty, expected j1")
	}

	// Review job should be in bundleB's queue.
	chB, _ := bundleB.Queue.Receive(ctx)
	select {
	case job := <-chB:
		if job.ID != "j2" {
			t.Errorf("bundleB got job %q, want j2", job.ID)
		}
	default:
		t.Error("bundleB queue empty, expected j2")
	}
}

func TestCoordinator_FallbackForEmptyTaskType(t *testing.T) {
	store := NewMemJobStore()
	bundle := NewInMemBundle(10, 1, store)
	defer bundle.Close()

	coord := NewCoordinator(bundle.Queue)
	coord.RegisterQueue("triage", bundle.Queue)

	ctx := context.Background()

	// Job with no TaskType goes to fallback.
	err := coord.Submit(ctx, &Job{ID: "j1", Priority: 50})
	if err != nil {
		t.Fatal(err)
	}

	ch, _ := bundle.Queue.Receive(ctx)
	select {
	case job := <-ch:
		if job.ID != "j1" {
			t.Errorf("got job %q, want j1", job.ID)
		}
	default:
		t.Error("fallback queue empty")
	}
}

func TestCoordinator_QueueDepthSumsAll(t *testing.T) {
	store := NewMemJobStore()
	bundleA := NewInMemBundle(10, 1, store)
	defer bundleA.Close()
	bundleB := NewInMemBundle(10, 1, store)
	defer bundleB.Close()

	coord := NewCoordinator(bundleA.Queue)
	coord.RegisterQueue("triage", bundleA.Queue)
	coord.RegisterQueue("review", bundleB.Queue)

	ctx := context.Background()
	coord.Submit(ctx, &Job{ID: "j1", TaskType: "triage", Priority: 50})
	coord.Submit(ctx, &Job{ID: "j2", TaskType: "review", Priority: 50})

	depth := coord.QueueDepth()
	if depth != 2 {
		t.Errorf("depth = %d, want 2", depth)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/queue/ -run TestCoordinator -v`
Expected: FAIL — `NewCoordinator` not defined

- [ ] **Step 3: Implement Coordinator**

Create `internal/queue/coordinator.go`:

```go
package queue

import "context"

// Coordinator implements JobQueue by routing Submit calls to the appropriate
// queue based on Job.TaskType. All other methods delegate to the fallback queue.
// Workflow code continues calling w.queue.Submit() unchanged.
type Coordinator struct {
	queues   map[string]JobQueue
	fallback JobQueue
}

func NewCoordinator(fallback JobQueue) *Coordinator {
	return &Coordinator{
		queues:   make(map[string]JobQueue),
		fallback: fallback,
	}
}

func (c *Coordinator) RegisterQueue(taskType string, q JobQueue) {
	c.queues[taskType] = q
}

func (c *Coordinator) Submit(ctx context.Context, job *Job) error {
	if job.TaskType != "" {
		if q, ok := c.queues[job.TaskType]; ok {
			return q.Submit(ctx, job)
		}
	}
	return c.fallback.Submit(ctx, job)
}

func (c *Coordinator) QueuePosition(jobID string) (int, error) {
	for _, q := range c.queues {
		pos, err := q.QueuePosition(jobID)
		if err == nil && pos > 0 {
			return pos, nil
		}
	}
	return c.fallback.QueuePosition(jobID)
}

func (c *Coordinator) QueueDepth() int {
	seen := make(map[JobQueue]bool)
	total := 0
	for _, q := range c.queues {
		if !seen[q] {
			total += q.QueueDepth()
			seen[q] = true
		}
	}
	if !seen[c.fallback] {
		total += c.fallback.QueueDepth()
	}
	return total
}

// Worker-side methods — delegate to fallback. These are not called via
// the Coordinator in practice (workers receive the actual queue directly),
// but implementing the full interface keeps the type satisfied.

func (c *Coordinator) Receive(ctx context.Context) (<-chan *Job, error) {
	return c.fallback.Receive(ctx)
}

func (c *Coordinator) Ack(ctx context.Context, jobID string) error {
	return c.fallback.Ack(ctx, jobID)
}

func (c *Coordinator) Register(ctx context.Context, info WorkerInfo) error {
	return c.fallback.Register(ctx, info)
}

func (c *Coordinator) Unregister(ctx context.Context, workerID string) error {
	return c.fallback.Unregister(ctx, workerID)
}

func (c *Coordinator) ListWorkers(ctx context.Context) ([]WorkerInfo, error) {
	return c.fallback.ListWorkers(ctx)
}

func (c *Coordinator) Close() error {
	return c.fallback.Close()
}
```

- [ ] **Step 4: Run coordinator tests**

Run: `go test ./internal/queue/ -run TestCoordinator -v`
Expected: all 3 tests pass

- [ ] **Step 5: Run all tests**

Run: `go test ./...`
Expected: all tests pass (Coordinator is new code, nothing references it yet)

- [ ] **Step 6: Commit**

```bash
git add internal/queue/coordinator.go internal/queue/coordinator_test.go
git commit -m "feat: add Coordinator as JobQueue decorator with TaskType routing"
```

---

### Task 5: Create Adapter interface + LocalAdapter

**Files:**
- Create: `internal/queue/adapter.go` (Adapter interface + AdapterDeps only)
- Create: `cmd/bot/local_adapter.go` (LocalAdapter + LocalAdapterConfig)

**Why split?** `internal/worker` imports `internal/queue`, so `queue` cannot import `worker`. The Adapter interface has no worker dependency. LocalAdapter references `worker.Runner` and `worker.RepoProvider`, so it lives in `cmd/bot/` (following the existing adapter pattern: `agentRunnerAdapter`, `repoCacheAdapter`, `slackPosterAdapter` are all in `cmd/bot/main.go`).

- [ ] **Step 1: Create Adapter interface in queue package**

Create `internal/queue/adapter.go`:

```go
package queue

// Adapter represents a pluggable execution backend.
type Adapter interface {
	Name() string
	Capabilities() []string
	Start(deps AdapterDeps) error
	Stop() error
}

// AdapterDeps contains only transport interfaces — shared by all adapter types.
type AdapterDeps struct {
	Jobs        JobQueue
	Results     ResultBus
	Status      StatusBus
	Commands    CommandBus
	Attachments AttachmentStore
}
```

- [ ] **Step 2: Create LocalAdapter in cmd/bot**

Create `cmd/bot/local_adapter.go`:

```go
package main

import (
	"context"
	"time"

	"slack-issue-bot/internal/queue"
	"slack-issue-bot/internal/worker"
)

// LocalAdapterConfig holds agent-specific configuration for the local adapter.
type LocalAdapterConfig struct {
	Runner         worker.Runner
	RepoCache      worker.RepoProvider
	SkillDirs      []string
	WorkerCount    int
	StatusInterval time.Duration
	Capabilities   []string
	Store          queue.JobStore
}

// LocalAdapter runs agents locally via worker.Pool.
// It implements queue.Adapter.
type LocalAdapter struct {
	cfg  LocalAdapterConfig
	pool *worker.Pool
}

func NewLocalAdapter(cfg LocalAdapterConfig) *LocalAdapter {
	return &LocalAdapter{cfg: cfg}
}

func (a *LocalAdapter) Name() string           { return "local" }
func (a *LocalAdapter) Capabilities() []string { return a.cfg.Capabilities }

func (a *LocalAdapter) Start(deps queue.AdapterDeps) error {
	a.pool = worker.NewPool(worker.Config{
		Queue:          deps.Jobs,
		Attachments:    deps.Attachments,
		Results:        deps.Results,
		Store:          a.cfg.Store,
		Runner:         a.cfg.Runner,
		RepoCache:      a.cfg.RepoCache,
		WorkerCount:    a.cfg.WorkerCount,
		SkillDirs:      a.cfg.SkillDirs,
		Commands:       deps.Commands,
		Status:         deps.Status,
		StatusInterval: a.cfg.StatusInterval,
	})
	a.pool.Start(context.Background())
	return nil
}

func (a *LocalAdapter) Stop() error {
	return nil
}
```

- [ ] **Step 3: Verify build**

Run: `go build ./...`
Expected: compiles successfully (no circular dependency)

- [ ] **Step 4: Run all tests**

Run: `go test ./...`
Expected: all tests pass (LocalAdapter is not wired in yet, existing integration tests cover the same flow)

- [ ] **Step 5: Commit**

```bash
git add internal/queue/adapter.go cmd/bot/local_adapter.go
git commit -m "feat: add Adapter interface and LocalAdapter wrapping worker.Pool"
```

---

### Task 6: Rewire main.go

**Files:**
- Modify: `cmd/bot/main.go`

- [ ] **Step 1: Replace worker pool creation with LocalAdapter + Coordinator**

In `cmd/bot/main.go`, replace the block from skill dir collection through `workerPool.Start` (lines 93-121) with:

```go
	// Determine skill dirs from active agent config.
	seen := make(map[string]bool)
	var skillDirs []string
	for _, name := range cfg.Fallback {
		if agent, ok := cfg.Agents[name]; ok && agent.SkillDir != "" && !seen[agent.SkillDir] {
			skillDirs = append(skillDirs, agent.SkillDir)
			seen[agent.SkillDir] = true
		}
	}
	if len(skillDirs) == 0 && cfg.ActiveAgent != "" {
		if agent, ok := cfg.Agents[cfg.ActiveAgent]; ok && agent.SkillDir != "" {
			skillDirs = append(skillDirs, agent.SkillDir)
		}
	}

	// Create Coordinator (JobQueue decorator for TaskType routing).
	coordinator := queue.NewCoordinator(bundle.Queue)
	coordinator.RegisterQueue("triage", bundle.Queue)

	// Create and start LocalAdapter (owns worker.Pool lifecycle).
	localAdapter := NewLocalAdapter(LocalAdapterConfig{
		Runner:         &agentRunnerAdapter{runner: agentRunner},
		RepoCache:      &repoCacheAdapter{cache: repoCache},
		SkillDirs:      skillDirs,
		WorkerCount:    cfg.Workers.Count,
		StatusInterval: cfg.Queue.StatusInterval,
		Capabilities:   []string{"triage"},
		Store:          jobStore,
	})
	localAdapter.Start(queue.AdapterDeps{
		Jobs:        bundle.Queue,
		Results:     bundle.Results,
		Status:      bundle.Status,
		Commands:    bundle.Commands,
		Attachments: bundle.Attachments,
	})
```

- [ ] **Step 2: Pass Coordinator to Workflow instead of bundle.Queue**

Change the Workflow creation line:

From:
```go
	wf := bot.NewWorkflow(cfg, slackClient, repoCache, repoDiscovery, agentRunner, mantisClient, bundle.Queue, jobStore, bundle.Attachments, skills)
```

To:
```go
	wf := bot.NewWorkflow(cfg, slackClient, repoCache, repoDiscovery, agentRunner, mantisClient, coordinator, jobStore, bundle.Attachments, skills)
```

- [ ] **Step 3: Pass Coordinator to StatusHandler**

Change the HTTP handler wiring:

From:
```go
			http.HandleFunc("/jobs", queue.StatusHandler(jobStore, bundle.Queue))
```

To:
```go
			http.HandleFunc("/jobs", queue.StatusHandler(jobStore, coordinator))
```

- [ ] **Step 4: Remove worker import from main.go if unused**

Check if `"slack-issue-bot/internal/worker"` appears in main.go imports. The `worker` package is now only imported by `local_adapter.go`, not `main.go`. If main.go still imports it, remove the import.

- [ ] **Step 5: Build**

Run: `go build ./...`
Expected: compiles successfully

- [ ] **Step 6: Run all tests**

Run: `go test ./...`
Expected: all tests pass

- [ ] **Step 7: Commit**

```bash
git add cmd/bot/main.go
git commit -m "refactor: wire Coordinator + LocalAdapter, remove direct worker.Pool creation"
```

---

### Task 7: End-to-end verification

- [ ] **Step 1: Run full test suite**

Run: `go test ./... -count=1`
Expected: all tests pass (count=1 disables test caching)

- [ ] **Step 2: Build and start bot**

Run: `go build -o bot ./cmd/bot/ && ./bot -config config.yaml`
Expected: bot starts, logs show "worker pool started" (from LocalAdapter's internal pool.Start)

- [ ] **Step 3: Trigger triage via Slack**

In Slack: mention @bot in a thread, select repo/branch, skip description.

Verify logs show:
- `prompt built`
- `trying agent`
- `agent registered`
- `agent process started`

- [ ] **Step 4: Monitor agent status**

Run: `curl -s localhost:8180/jobs | jq ".jobs[0].agent"`

Expected: shows `pid`, `command`, `alive`, `tool_calls`, `files_read`, etc. (same as before refactor).

- [ ] **Step 5: Let job complete or cancel**

Either wait for issue creation or press cancel button.

Verify:
- Issue created in GitHub (if completed)
- Status message updated in Slack (not a new message)
- `/jobs` shows completed/failed status with cost data

- [ ] **Step 6: Final commit with verification note**

```bash
git add -A
git commit -m "docs: Phase A refactor complete - verified end-to-end"
```
