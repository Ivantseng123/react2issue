# Worker Liveness Precheck Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Spec:** `docs/superpowers/specs/2026-04-22-worker-liveness-precheck-design.md`

**Goal:** Wire a two-stage worker availability precheck into the Slack triage flow so that the user is told (a) at trigger time when no workers are online, and (b) at submit time gets a hard reject (no workers) or an ETA (busy) — replacing today's silent wait that ends in a watchdog timeout.

**Architecture:** New `shared/queue.WorkerAvailability` service produces a typed `Verdict` from `JobQueue.ListWorkers`, `JobQueue.QueueDepth`, and `JobStore.ListAll`. The Slack adapter (`app/bot/workflow.go`, post-dispatcher refactor) calls `CheckSoft` in `HandleTrigger` (non-blocking warn) and `CheckHard` inside a new `submit()` helper that consolidates the three current `executeStep` → `onSubmit` call sites. A small `app/bot/verdict_message.go` translates `Verdict` into Slack-flavored text — the seam where future mediums plug in their own renderers. `workflow.Pending` gains an exported `BusyHint` field so the `submitJob` closure in `app/app.go` can append the ETA hint to the lifecycle status text returned by `BuildJob`.

**Tech Stack:** Go 1.22+ / `slog` / `prometheus/client_golang` / existing `shared/queue` Redis transport / `queuetest` in-memory bundle for unit tests.

**Phases (one commit per task):**
1. Data model — `WorkerInfo.Slots`
2. Availability service — TDD growth (5 tasks)
3. Metrics
4. Verdict rendering
5. Workflow integration (4 tasks: wiring → submit() helper + NoWorkers → BusyHint on busy → soft warn)
6. Worker side
7. App wiring + config (config struct → app.go construction + BusyHint append)
8. Final verification

---

## Phase 1 — Data Model

### Task 1: Add `Slots` field to `WorkerInfo`

**Files:**
- Modify: `shared/queue/job.go` (`WorkerInfo` struct, around line 111)

- [ ] **Step 1: Add the field**

In `shared/queue/job.go`, replace the existing `WorkerInfo` struct (the block beginning `type WorkerInfo struct {`) with:

```go
type WorkerInfo struct {
	WorkerID    string   `json:"worker_id"`
	Name        string   `json:"name"`
	Nickname    string   `json:"nickname,omitempty"`
	Agents      []string `json:"agents"`
	Tags        []string `json:"tags"`
	Slots       int      `json:"slots,omitempty"` // concurrent jobs this worker handles; 0 normalised to 1 by consumers
	ConnectedAt time.Time
}
```

- [ ] **Step 2: Verify the change compiles**

Run: `cd shared && go build ./...`
Expected: no output (success). Any callers building `WorkerInfo` literals continue to compile because `Slots` is positional-after-existing-fields and unset → `0`.

- [ ] **Step 3: Run existing queue tests (regression check)**

Run: `cd shared && go test ./queue/...`
Expected: all existing tests pass; `redis_jobqueue_test.go` JSON round-trip continues to work because `omitempty` on a zero int omits the field.

- [ ] **Step 4: Commit**

```bash
git add shared/queue/job.go
git commit -m "$(cat <<'EOF'
feat(queue): add Slots field to WorkerInfo

Per-worker concurrency declaration; zero value normalised to 1 by
consumers so existing single-job workers continue to work.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Phase 2 — Availability Service (TDD)

### Task 2: Skeleton + healthy-path verdict

**Files:**
- Create: `shared/queue/availability.go`
- Create: `shared/queue/availability_test.go`

- [ ] **Step 1: Write the failing test**

Create `shared/queue/availability_test.go`:

```go
package queue_test

import (
	"context"
	"testing"
	"time"

	"github.com/Ivantseng123/agentdock/shared/queue"
	"github.com/Ivantseng123/agentdock/shared/queue/queuetest"
)

func newAvail(t *testing.T) (*queuetest.JobQueue, queue.JobStore, queue.WorkerAvailability) {
	t.Helper()
	store := queue.NewMemJobStore()
	q := queuetest.NewJobQueue(50, store)
	a := queue.NewWorkerAvailability(q, store, queue.AvailabilityConfig{
		AvgJobDuration: 3 * time.Minute,
	})
	return q, store, a
}

func TestAvailability_HealthyOK(t *testing.T) {
	q, _, a := newAvail(t)

	q.Register(context.Background(), queue.WorkerInfo{WorkerID: "w1", Slots: 1})
	q.Register(context.Background(), queue.WorkerInfo{WorkerID: "w2", Slots: 1})

	v := a.CheckHard(context.Background())
	if v.Kind != queue.VerdictOK {
		t.Errorf("Kind = %q, want %q", v.Kind, queue.VerdictOK)
	}
	if v.WorkerCount != 2 {
		t.Errorf("WorkerCount = %d, want 2", v.WorkerCount)
	}
	if v.TotalSlots != 2 {
		t.Errorf("TotalSlots = %d, want 2", v.TotalSlots)
	}
}
```

- [ ] **Step 2: Run test — should fail (no symbols defined yet)**

Run: `cd shared && go test ./queue/ -run TestAvailability_HealthyOK -v`
Expected: FAIL — `undefined: queue.WorkerAvailability`, `undefined: queue.NewWorkerAvailability`, etc.

- [ ] **Step 3: Implement minimal availability service**

Create `shared/queue/availability.go`:

```go
package queue

import (
	"context"
	"log/slog"
	"time"
)

type VerdictKind string

const (
	VerdictOK            VerdictKind = "ok"
	VerdictBusyEnqueueOK VerdictKind = "busy_enqueue"
	VerdictNoWorkers     VerdictKind = "no_workers"
)

type Verdict struct {
	Kind          VerdictKind
	WorkerCount   int
	ActiveJobs    int
	TotalSlots    int
	EstimatedWait time.Duration
}

type WorkerAvailability interface {
	CheckSoft(ctx context.Context) Verdict
	CheckHard(ctx context.Context) Verdict
}

type AvailabilityConfig struct {
	AvgJobDuration time.Duration
}

type availability struct {
	queue   JobQueue
	store   JobStore
	avgJob  time.Duration
	logger  *slog.Logger
}

func NewWorkerAvailability(q JobQueue, store JobStore, cfg AvailabilityConfig) WorkerAvailability {
	avg := cfg.AvgJobDuration
	if avg <= 0 {
		avg = 3 * time.Minute
	}
	return &availability{
		queue:  q,
		store:  store,
		avgJob: avg,
		logger: slog.Default(),
	}
}

func (a *availability) CheckSoft(ctx context.Context) Verdict { return a.compute(ctx) }
func (a *availability) CheckHard(ctx context.Context) Verdict { return a.compute(ctx) }

func (a *availability) compute(ctx context.Context) Verdict {
	workers, err := a.queue.ListWorkers(ctx)
	if err != nil {
		a.logger.Warn("availability: ListWorkers failed", "error", err)
		return Verdict{Kind: VerdictOK}
	}
	totalSlots := 0
	for _, w := range workers {
		totalSlots += normaliseSlots(w.Slots)
	}
	return Verdict{
		Kind:        VerdictOK,
		WorkerCount: len(workers),
		TotalSlots:  totalSlots,
	}
}

func normaliseSlots(s int) int {
	if s <= 0 {
		return 1
	}
	return s
}
```

- [ ] **Step 4: Run test — should pass**

Run: `cd shared && go test ./queue/ -run TestAvailability_HealthyOK -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add shared/queue/availability.go shared/queue/availability_test.go
git commit -m "$(cat <<'EOF'
feat(queue): introduce WorkerAvailability service skeleton

Verdict types and OK-path computation. Subsequent commits add
NoWorkers, BusyEnqueueOK, slots normalisation, and fail-open paths.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

### Task 3: NoWorkers verdict

**Files:**
- Modify: `shared/queue/availability.go`
- Modify: `shared/queue/availability_test.go`

- [ ] **Step 1: Write the failing test**

Append to `shared/queue/availability_test.go`:

```go
func TestAvailability_NoWorkers(t *testing.T) {
	_, _, a := newAvail(t)

	v := a.CheckHard(context.Background())
	if v.Kind != queue.VerdictNoWorkers {
		t.Errorf("Kind = %q, want %q", v.Kind, queue.VerdictNoWorkers)
	}
	if v.WorkerCount != 0 {
		t.Errorf("WorkerCount = %d, want 0", v.WorkerCount)
	}
}
```

- [ ] **Step 2: Run — should fail (still returns OK)**

Run: `cd shared && go test ./queue/ -run TestAvailability_NoWorkers -v`
Expected: FAIL — `Kind = "ok", want "no_workers"`.

- [ ] **Step 3: Add the NoWorkers branch in `compute`**

In `shared/queue/availability.go`, replace the body of `compute` with:

```go
func (a *availability) compute(ctx context.Context) Verdict {
	workers, err := a.queue.ListWorkers(ctx)
	if err != nil {
		a.logger.Warn("availability: ListWorkers failed", "error", err)
		return Verdict{Kind: VerdictOK}
	}
	totalSlots := 0
	for _, w := range workers {
		totalSlots += normaliseSlots(w.Slots)
	}
	if len(workers) == 0 {
		return Verdict{Kind: VerdictNoWorkers}
	}
	return Verdict{
		Kind:        VerdictOK,
		WorkerCount: len(workers),
		TotalSlots:  totalSlots,
	}
}
```

- [ ] **Step 4: Run both tests**

Run: `cd shared && go test ./queue/ -run TestAvailability -v`
Expected: PASS for `TestAvailability_HealthyOK` and `TestAvailability_NoWorkers`.

- [ ] **Step 5: Commit**

```bash
git add shared/queue/availability.go shared/queue/availability_test.go
git commit -m "$(cat <<'EOF'
feat(queue): availability returns NoWorkers when no workers registered

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

### Task 4: BusyEnqueueOK verdict + ETA

**Files:**
- Modify: `shared/queue/availability.go`
- Modify: `shared/queue/availability_test.go`

- [ ] **Step 1: Write the failing test**

Append:

```go
func TestAvailability_BusyEnqueueOK_FullySaturated(t *testing.T) {
	q, store, a := newAvail(t)
	ctx := context.Background()

	q.Register(ctx, queue.WorkerInfo{WorkerID: "w1", Slots: 1})
	q.Register(ctx, queue.WorkerInfo{WorkerID: "w2", Slots: 1})

	// Two jobs in JobRunning consume both slots; queue depth = 0.
	store.Put(&queue.Job{ID: "j1"})
	store.UpdateStatus("j1", queue.JobRunning)
	store.Put(&queue.Job{ID: "j2"})
	store.UpdateStatus("j2", queue.JobRunning)

	v := a.CheckHard(ctx)
	if v.Kind != queue.VerdictBusyEnqueueOK {
		t.Errorf("Kind = %q, want %q", v.Kind, queue.VerdictBusyEnqueueOK)
	}
	if v.ActiveJobs != 2 {
		t.Errorf("ActiveJobs = %d, want 2", v.ActiveJobs)
	}
	wantETA := 1 * 3 * time.Minute // overflow=1
	if v.EstimatedWait != wantETA {
		t.Errorf("EstimatedWait = %v, want %v", v.EstimatedWait, wantETA)
	}
}

func TestAvailability_BusyEnqueueOK_WithQueueDepth(t *testing.T) {
	q, store, a := newAvail(t)
	ctx := context.Background()

	q.Register(ctx, queue.WorkerInfo{WorkerID: "w1", Slots: 1})

	// 1 running + 4 queued = 5 active, slots = 1, overflow = 5
	store.Put(&queue.Job{ID: "j1"})
	store.UpdateStatus("j1", queue.JobRunning)
	for i := 0; i < 4; i++ {
		q.Submit(ctx, &queue.Job{ID: "p" + string(rune('a'+i))})
	}

	v := a.CheckHard(ctx)
	if v.Kind != queue.VerdictBusyEnqueueOK {
		t.Errorf("Kind = %q, want %q", v.Kind, queue.VerdictBusyEnqueueOK)
	}
	wantETA := time.Duration(5) * 3 * time.Minute
	if v.EstimatedWait != wantETA {
		t.Errorf("EstimatedWait = %v, want %v", v.EstimatedWait, wantETA)
	}
}
```

- [ ] **Step 2: Run — should fail (active count not yet computed)**

Run: `cd shared && go test ./queue/ -run TestAvailability_BusyEnqueueOK -v`
Expected: FAIL — verdict still returns `OK` because the busy branch is missing.

- [ ] **Step 3: Add active-jobs computation + busy branch**

Replace `compute` body:

```go
func (a *availability) compute(ctx context.Context) Verdict {
	workers, err := a.queue.ListWorkers(ctx)
	if err != nil {
		a.logger.Warn("availability: ListWorkers failed", "error", err)
		return Verdict{Kind: VerdictOK}
	}
	totalSlots := 0
	for _, w := range workers {
		totalSlots += normaliseSlots(w.Slots)
	}
	if len(workers) == 0 {
		return Verdict{Kind: VerdictNoWorkers}
	}

	depth := a.queue.QueueDepth()
	states, err := a.store.ListAll()
	if err != nil {
		a.logger.Warn("availability: ListAll failed", "error", err)
		return Verdict{Kind: VerdictOK, WorkerCount: len(workers), TotalSlots: totalSlots}
	}
	running := 0
	for _, s := range states {
		if s.Status == JobPreparing || s.Status == JobRunning {
			running++
		}
	}
	active := depth + running

	if active >= totalSlots {
		overflow := active - totalSlots + 1
		return Verdict{
			Kind:          VerdictBusyEnqueueOK,
			WorkerCount:   len(workers),
			TotalSlots:    totalSlots,
			ActiveJobs:    active,
			EstimatedWait: time.Duration(overflow) * a.avgJob,
		}
	}
	return Verdict{
		Kind:        VerdictOK,
		WorkerCount: len(workers),
		TotalSlots:  totalSlots,
		ActiveJobs:  active,
	}
}
```

- [ ] **Step 4: Run all availability tests**

Run: `cd shared && go test ./queue/ -run TestAvailability -v`
Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add shared/queue/availability.go shared/queue/availability_test.go
git commit -m "$(cat <<'EOF'
feat(queue): availability returns BusyEnqueueOK with ETA when saturated

ETA = (active - slots + 1) * AvgJobDuration. Coarse intentional —
spec is signal "you'll wait", not minute-accurate prediction.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

### Task 5: Multi-slot + zero-slot normalisation

**Files:**
- Modify: `shared/queue/availability_test.go`

- [ ] **Step 1: Write the failing tests**

Append:

```go
func TestAvailability_MultiSlotWorker(t *testing.T) {
	q, store, a := newAvail(t)
	ctx := context.Background()

	// One worker with 3 slots, two running jobs → 1 spare slot → OK.
	q.Register(ctx, queue.WorkerInfo{WorkerID: "w1", Slots: 3})
	store.Put(&queue.Job{ID: "j1"})
	store.UpdateStatus("j1", queue.JobRunning)
	store.Put(&queue.Job{ID: "j2"})
	store.UpdateStatus("j2", queue.JobRunning)

	v := a.CheckHard(ctx)
	if v.Kind != queue.VerdictOK {
		t.Errorf("Kind = %q, want %q (3 slots, 2 active)", v.Kind, queue.VerdictOK)
	}
	if v.TotalSlots != 3 {
		t.Errorf("TotalSlots = %d, want 3", v.TotalSlots)
	}
}

func TestAvailability_ZeroSlotsNormalisedToOne(t *testing.T) {
	q, _, a := newAvail(t)
	ctx := context.Background()

	// Slots=0 (e.g. older worker that didn't set the field) → treated as 1.
	q.Register(ctx, queue.WorkerInfo{WorkerID: "old", Slots: 0})

	v := a.CheckHard(ctx)
	if v.TotalSlots != 1 {
		t.Errorf("TotalSlots = %d, want 1 (normalised)", v.TotalSlots)
	}
	if v.Kind != queue.VerdictOK {
		t.Errorf("Kind = %q, want %q", v.Kind, queue.VerdictOK)
	}
}
```

- [ ] **Step 2: Run — these should already pass**

Run: `cd shared && go test ./queue/ -run TestAvailability -v`
Expected: PASS for both new tests. (`normaliseSlots` is already in place from Task 2; this task locks that behavior with explicit tests.)

- [ ] **Step 3: Commit**

```bash
git add shared/queue/availability_test.go
git commit -m "$(cat <<'EOF'
test(queue): lock multi-slot and zero-slot normalisation behaviour

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

### Task 6: Fail-open on dependency errors

**Files:**
- Create: `shared/queue/availability_failopen_test.go` (separate file because we need a stub `JobQueue` that returns errors, distinct from `queuetest.JobQueue`)

- [ ] **Step 1: Write the failing tests**

Create `shared/queue/availability_failopen_test.go`:

```go
package queue_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Ivantseng123/agentdock/shared/queue"
)

// erroringQueue satisfies queue.JobQueue but returns errors on the methods
// availability depends on. Other methods panic — they should never be called
// by the availability service.
type erroringQueue struct {
	listWorkersErr error
}

func (e *erroringQueue) Submit(context.Context, *queue.Job) error  { panic("unused") }
func (e *erroringQueue) QueuePosition(string) (int, error)          { panic("unused") }
func (e *erroringQueue) QueueDepth() int                            { return 0 }
func (e *erroringQueue) Receive(context.Context) (<-chan *queue.Job, error) {
	panic("unused")
}
func (e *erroringQueue) Ack(context.Context, string) error            { panic("unused") }
func (e *erroringQueue) Register(context.Context, queue.WorkerInfo) error {
	panic("unused")
}
func (e *erroringQueue) Unregister(context.Context, string) error { panic("unused") }
func (e *erroringQueue) ListWorkers(context.Context) ([]queue.WorkerInfo, error) {
	if e.listWorkersErr != nil {
		return nil, e.listWorkersErr
	}
	return nil, nil
}
func (e *erroringQueue) Close() error { return nil }

// erroringStore satisfies queue.JobStore but errors on ListAll.
type erroringStore struct {
	listAllErr error
}

func (s *erroringStore) Put(*queue.Job) error                    { panic("unused") }
func (s *erroringStore) Get(string) (*queue.JobState, error)      { panic("unused") }
func (s *erroringStore) GetByThread(string, string) (*queue.JobState, error) {
	panic("unused")
}
func (s *erroringStore) ListPending() ([]*queue.JobState, error) { panic("unused") }
func (s *erroringStore) UpdateStatus(string, queue.JobStatus) error {
	panic("unused")
}
func (s *erroringStore) SetWorker(string, string) error { panic("unused") }
func (s *erroringStore) SetAgentStatus(string, queue.StatusReport) error {
	panic("unused")
}
func (s *erroringStore) Delete(string) error { panic("unused") }
func (s *erroringStore) ListAll() ([]*queue.JobState, error) {
	if s.listAllErr != nil {
		return nil, s.listAllErr
	}
	return nil, nil
}

func TestAvailability_FailOpen_ListWorkersError(t *testing.T) {
	q := &erroringQueue{listWorkersErr: errors.New("redis down")}
	store := queue.NewMemJobStore()
	a := queue.NewWorkerAvailability(q, store, queue.AvailabilityConfig{
		AvgJobDuration: 3 * time.Minute,
	})

	v := a.CheckHard(context.Background())
	if v.Kind != queue.VerdictOK {
		t.Errorf("Kind = %q, want %q (fail-open)", v.Kind, queue.VerdictOK)
	}
}

func TestAvailability_FailOpen_ListAllError(t *testing.T) {
	// Need a queue that returns at least one worker (so we pass the NoWorkers gate)
	// AND a store that errors on ListAll.
	q := &workerListingQueue{erroringQueue: erroringQueue{}, workers: []queue.WorkerInfo{{WorkerID: "w1"}}}
	store := &erroringStore{listAllErr: errors.New("store down")}
	a := queue.NewWorkerAvailability(q, store, queue.AvailabilityConfig{
		AvgJobDuration: 3 * time.Minute,
	})

	v := a.CheckHard(context.Background())
	if v.Kind != queue.VerdictOK {
		t.Errorf("Kind = %q, want %q (fail-open on store error)", v.Kind, queue.VerdictOK)
	}
}

// workerListingQueue is erroringQueue but with a configurable workers list.
type workerListingQueue struct {
	erroringQueue
	workers []queue.WorkerInfo
}

func (w *workerListingQueue) ListWorkers(context.Context) ([]queue.WorkerInfo, error) {
	return w.workers, nil
}
```

- [ ] **Step 2: Run — `TestAvailability_FailOpen_ListWorkersError` already passes, `TestAvailability_FailOpen_ListAllError` should pass too**

Run: `cd shared && go test ./queue/ -run TestAvailability_FailOpen -v`
Expected: PASS for both. The `compute` function written in Task 4 already returns `OK` on `ListAll` error before the busy branch is evaluated.

- [ ] **Step 3: Commit**

```bash
git add shared/queue/availability_failopen_test.go
git commit -m "$(cat <<'EOF'
test(queue): lock availability fail-open behaviour for dep errors

Treat ListWorkers / ListAll errors as transient blindness; return OK
rather than mis-classify the system as broken. The watchdog backstops.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Phase 3 — Metrics

### Task 7: Add availability metrics + register + wire

**Files:**
- Modify: `shared/metrics/metrics.go` (`WatchdogKillsTotal` ~line 135, `Register` ~line 165)
- Modify: `shared/queue/availability.go`

- [ ] **Step 1: Add the three new metrics in `shared/metrics/metrics.go`**

After the existing `WatchdogKillsTotal` declaration (around line 141), insert:

```go
// ---- Availability ----

var WorkerAvailabilityVerdictTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
	Namespace: namespace,
	Name:      "worker_availability_verdict_total",
	Help:      "Counts of availability verdicts by kind and stage.",
}, []string{"kind", "stage"})

var WorkerAvailabilityCheckDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
	Namespace: namespace,
	Name:      "worker_availability_check_duration_seconds",
	Help:      "Latency of WorkerAvailability.compute.",
	Buckets:   []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1},
})

var WorkerAvailabilityCheckErrors = prometheus.NewCounterVec(prometheus.CounterOpts{
	Namespace: namespace,
	Name:      "worker_availability_check_errors_total",
	Help:      "Errors from availability dependencies.",
}, []string{"dependency"})
```

Then in the `reg.MustRegister(...)` block in `Register` (around line 173–194), add the three new metrics:

```go
reg.MustRegister(
	RequestTotal,
	// ... existing ...
	ExternalErrorsTotal,
	WorkerAvailabilityVerdictTotal,    // NEW
	WorkerAvailabilityCheckDuration,    // NEW
	WorkerAvailabilityCheckErrors,      // NEW
)
```

- [ ] **Step 2: Verify metrics package compiles & tests still pass**

Run: `cd shared && go test ./metrics/ -v`
Expected: PASS.

- [ ] **Step 3: Wire metrics into availability via a hook (avoid import cycle)**

`shared/queue` cannot import `shared/metrics` (would create a cycle: metrics imports queue for the GaugeFunc). Use the same pattern as `WithWatchdogKillHook`.

In `shared/queue/availability.go`, replace the file with:

```go
package queue

import (
	"context"
	"log/slog"
	"time"
)

type VerdictKind string

const (
	VerdictOK            VerdictKind = "ok"
	VerdictBusyEnqueueOK VerdictKind = "busy_enqueue"
	VerdictNoWorkers     VerdictKind = "no_workers"
)

type Verdict struct {
	Kind          VerdictKind
	WorkerCount   int
	ActiveJobs    int
	TotalSlots    int
	EstimatedWait time.Duration
}

type WorkerAvailability interface {
	CheckSoft(ctx context.Context) Verdict
	CheckHard(ctx context.Context) Verdict
}

type AvailabilityConfig struct {
	AvgJobDuration time.Duration
}

// AvailabilityOption configures observability hooks without creating an
// import cycle with shared/metrics.
type AvailabilityOption func(*availability)

// WithVerdictHook is invoked on every compute() call with kind, stage, and duration.
func WithVerdictHook(fn func(kind, stage string, d time.Duration)) AvailabilityOption {
	return func(a *availability) { a.verdictHook = fn }
}

// WithDepErrorHook is invoked when a dependency call fails, with the
// dependency name (e.g. "list_workers", "list_all").
func WithDepErrorHook(fn func(dep string)) AvailabilityOption {
	return func(a *availability) { a.depErrorHook = fn }
}

type availability struct {
	queue        JobQueue
	store        JobStore
	avgJob       time.Duration
	logger       *slog.Logger
	verdictHook  func(kind, stage string, d time.Duration)
	depErrorHook func(dep string)
}

func NewWorkerAvailability(q JobQueue, store JobStore, cfg AvailabilityConfig, opts ...AvailabilityOption) WorkerAvailability {
	avg := cfg.AvgJobDuration
	if avg <= 0 {
		avg = 3 * time.Minute
	}
	a := &availability{
		queue:  q,
		store:  store,
		avgJob: avg,
		logger: slog.Default(),
	}
	for _, o := range opts {
		o(a)
	}
	return a
}

func (a *availability) CheckSoft(ctx context.Context) Verdict {
	return a.observe(ctx, "soft")
}

func (a *availability) CheckHard(ctx context.Context) Verdict {
	return a.observe(ctx, "hard")
}

func (a *availability) observe(ctx context.Context, stage string) Verdict {
	start := time.Now()
	v := a.compute(ctx)
	if a.verdictHook != nil {
		a.verdictHook(string(v.Kind), stage, time.Since(start))
	}
	return v
}

func (a *availability) compute(ctx context.Context) Verdict {
	workers, err := a.queue.ListWorkers(ctx)
	if err != nil {
		a.logger.Warn("availability: ListWorkers failed", "error", err)
		if a.depErrorHook != nil {
			a.depErrorHook("list_workers")
		}
		return Verdict{Kind: VerdictOK}
	}
	totalSlots := 0
	for _, w := range workers {
		totalSlots += normaliseSlots(w.Slots)
	}
	if len(workers) == 0 {
		return Verdict{Kind: VerdictNoWorkers}
	}

	depth := a.queue.QueueDepth()
	states, err := a.store.ListAll()
	if err != nil {
		a.logger.Warn("availability: ListAll failed", "error", err)
		if a.depErrorHook != nil {
			a.depErrorHook("list_all")
		}
		return Verdict{Kind: VerdictOK, WorkerCount: len(workers), TotalSlots: totalSlots}
	}
	running := 0
	for _, s := range states {
		if s.Status == JobPreparing || s.Status == JobRunning {
			running++
		}
	}
	active := depth + running

	if active >= totalSlots {
		overflow := active - totalSlots + 1
		return Verdict{
			Kind:          VerdictBusyEnqueueOK,
			WorkerCount:   len(workers),
			TotalSlots:    totalSlots,
			ActiveJobs:    active,
			EstimatedWait: time.Duration(overflow) * a.avgJob,
		}
	}
	return Verdict{
		Kind:        VerdictOK,
		WorkerCount: len(workers),
		TotalSlots:  totalSlots,
		ActiveJobs:  active,
	}
}

func normaliseSlots(s int) int {
	if s <= 0 {
		return 1
	}
	return s
}
```

- [ ] **Step 4: Run availability tests**

Run: `cd shared && go test ./queue/ -run TestAvailability -v`
Expected: all PASS (no test depends on the hook being set; default nil).

- [ ] **Step 5: Commit**

```bash
git add shared/queue/availability.go shared/metrics/metrics.go
git commit -m "$(cat <<'EOF'
feat(metrics): WorkerAvailability verdict + duration + dep errors

Wired via functional options (verdict hook + dep-error hook) to avoid
introducing a shared/queue → shared/metrics import cycle.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Phase 4 — Verdict Rendering

### Task 8: `verdict_message.go` + tests

**Files:**
- Create: `app/bot/verdict_message.go`
- Create: `app/bot/verdict_message_test.go`

- [ ] **Step 1: Write the failing test**

Create `app/bot/verdict_message_test.go`:

```go
package bot

import (
	"strings"
	"testing"
	"time"

	"github.com/Ivantseng123/agentdock/shared/queue"
)

func TestRenderSoftWarn_NoWorkers(t *testing.T) {
	got := RenderSoftWarn(queue.Verdict{Kind: queue.VerdictNoWorkers})
	if !strings.Contains(got, ":warning:") {
		t.Errorf("missing :warning: prefix; got %q", got)
	}
	if !strings.Contains(got, "沒有 worker") {
		t.Errorf("missing key phrase '沒有 worker'; got %q", got)
	}
}

func TestRenderHardReject_NoWorkers(t *testing.T) {
	got := RenderHardReject(queue.Verdict{Kind: queue.VerdictNoWorkers})
	if !strings.Contains(got, ":x:") {
		t.Errorf("missing :x: prefix; got %q", got)
	}
	if !strings.Contains(got, "無法處理") {
		t.Errorf("missing '無法處理'; got %q", got)
	}
}

func TestRenderBusyHint_WithETA(t *testing.T) {
	v := queue.Verdict{
		Kind:          queue.VerdictBusyEnqueueOK,
		EstimatedWait: 9 * time.Minute,
	}
	got := RenderBusyHint(v)
	if !strings.Contains(got, "預估等候") {
		t.Errorf("missing '預估等候'; got %q", got)
	}
	if !strings.Contains(got, "9m") {
		t.Errorf("expected '9m' in output; got %q", got)
	}
}

func TestRenderBusyHint_ZeroETA_ReturnsEmpty(t *testing.T) {
	v := queue.Verdict{Kind: queue.VerdictBusyEnqueueOK, EstimatedWait: 0}
	if got := RenderBusyHint(v); got != "" {
		t.Errorf("expected empty string for zero ETA; got %q", got)
	}
}
```

- [ ] **Step 2: Run — should fail**

Run: `cd app && go test ./bot/ -run "TestRender" -v`
Expected: FAIL — `undefined: RenderSoftWarn`, etc.

- [ ] **Step 3: Implement renderers**

Create `app/bot/verdict_message.go`:

```go
package bot

import (
	"fmt"
	"time"

	"github.com/Ivantseng123/agentdock/shared/queue"
)

// RenderSoftWarn produces the trigger-time soft warning. Currently only the
// NoWorkers verdict is rendered; other verdicts return "" (caller should not
// post empty messages).
func RenderSoftWarn(v queue.Verdict) string {
	if v.Kind != queue.VerdictNoWorkers {
		return ""
	}
	return ":warning: 目前沒有 worker 在線，你仍可繼續選擇，送出時會再確認一次。"
}

// RenderHardReject produces the submit-time rejection message.
func RenderHardReject(v queue.Verdict) string {
	if v.Kind != queue.VerdictNoWorkers {
		return ""
	}
	return ":x: 目前沒有 worker 在線，無法處理。請稍後再試。"
}

// RenderBusyHint produces the suffix appended to the lifecycle queue
// message when the verdict is BusyEnqueueOK with a non-zero ETA.
func RenderBusyHint(v queue.Verdict) string {
	if v.EstimatedWait <= 0 {
		return ""
	}
	return fmt.Sprintf("(預估等候 ~%dm)",
		int(v.EstimatedWait.Round(time.Minute).Minutes()))
}
```

- [ ] **Step 4: Run renderer tests — should pass**

Run: `cd app && go test ./bot/ -run "TestRender" -v`
Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add app/bot/verdict_message.go app/bot/verdict_message_test.go
git commit -m "$(cat <<'EOF'
feat(bot): verdict_message renders Slack text from queue.Verdict

Single seam where future mediums (X, Discord) plug in their own
renderers. Today's text is intentionally minimal — oncall handles
or richer guidance can be added without touching the verdict service.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Phase 5 — Workflow Integration

### Task 9: Add availability wiring + `Pending.BusyHint` (no behaviour yet)

**Files:**
- Modify: `app/workflow/workflow.go` (`Pending` struct, ~line 30)
- Modify: `app/bot/workflow.go` (`Workflow` struct ~line 19, `NewWorkflow` ~line 38)
- Modify: `app/bot/workflow_test.go` (5 existing `NewWorkflow` call sites need a trailing `nil`)
- Modify: `app/app.go` (`bot.NewWorkflow(...)` call ~line 170 — append `nil` placeholder until Task 15)

- [ ] **Step 1: Add `BusyHint` to `workflow.Pending`**

In `app/workflow/workflow.go`, append a `BusyHint` field to the `Pending` struct:

```go
type Pending struct {
	ChannelID   string
	ThreadTS    string
	TriggerTS   string
	UserID      string
	Reporter    string
	ChannelName string
	RequestID   string
	SelectorTS  string // TS of the latest selector/modal message; used as pending-map key
	Phase       string // workflow-defined phase label
	TaskType    string // workflow identity, equal to Workflow.Type()
	State       any    // per-workflow state struct
	BusyHint    string // populated by bot.Workflow.submit() when verdict is BusyEnqueueOK; app.submitJob appends to statusText
}
```

- [ ] **Step 2: Add `availability` field + extend `NewWorkflow` signature in `app/bot/workflow.go`**

(a) Add the field to the `Workflow` struct (around line 19):

```go
type Workflow struct {
	cfg           *config.Config
	dispatcher    *workflow.Dispatcher
	slack         workflow.SlackPort
	handler       *slackclient.Handler
	repoDiscovery *ghclient.RepoDiscovery
	logger        *slog.Logger
	availability  queue.WorkerAvailability // NEW

	mu        sync.Mutex
	pending   map[string]*workflow.Pending
	autoBound map[string]bool

	onSubmit func(ctx context.Context, p *workflow.Pending)
}
```

(b) Add `"github.com/Ivantseng123/agentdock/shared/queue"` to the imports if not already present.

(c) Update `NewWorkflow` to accept availability as the LAST parameter:

```go
func NewWorkflow(
	cfg *config.Config,
	dispatcher *workflow.Dispatcher,
	slack workflow.SlackPort,
	repoDiscovery *ghclient.RepoDiscovery,
	logger *slog.Logger,
	availability queue.WorkerAvailability, // NEW
) *Workflow {
	return &Workflow{
		cfg:           cfg,
		dispatcher:    dispatcher,
		slack:         slack,
		repoDiscovery: repoDiscovery,
		logger:        logger,
		availability:  availability, // NEW
		pending:       make(map[string]*workflow.Pending),
		autoBound:     make(map[string]bool),
	}
}
```

- [ ] **Step 3: Update existing `NewWorkflow` call sites in tests**

`app/bot/workflow_test.go` has 5 calls of the form `NewWorkflow(cfg, disp, slack, nil, nil)` (search for `:= NewWorkflow(`). Append a trailing `nil` argument to each so they pass `nil` for the new `availability` parameter:

```go
wf := NewWorkflow(cfg, disp, slack, nil, nil, nil)
```

Also in `app/bot/workflow_test.go:222` the call is `NewWorkflow(cfg, disp, sl, nil, slog.Default())` — becomes `NewWorkflow(cfg, disp, sl, nil, slog.Default(), nil)`.

- [ ] **Step 4: Update the production caller in `app/app.go`**

In `app/app.go` around line 170, the current call is:

```go
wf := bot.NewWorkflow(cfg, dispatcher, slackPort, repoDiscovery, appLogger)
```

Append a `nil` (Task 15 replaces this with a real instance):

```go
wf := bot.NewWorkflow(cfg, dispatcher, slackPort, repoDiscovery, appLogger, nil)
```

- [ ] **Step 5: Verify the whole tree compiles**

Run: `go build ./...`
Expected: success.

- [ ] **Step 6: Run all tests (regression)**

Run: `go test ./...`
Expected: all PASS. The new `availability` field is nil; no existing test touches availability. New tests in Tasks 10–12 will set a stub.

- [ ] **Step 7: Commit**

```bash
git add app/workflow/workflow.go app/bot/workflow.go app/bot/workflow_test.go app/app.go
git commit -m "$(cat <<'EOF'
refactor(bot+workflow): add availability wiring + Pending.BusyHint

No behaviour change. Workflow constructor gains a nil-allowed
availability parameter and Pending gains a BusyHint field; the
hard/soft check call sites land in subsequent commits.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

### Task 10: `Workflow.submit()` helper + NoWorkers hard reject

**Files:**
- Modify: `app/bot/workflow.go` (`executeStep` ~line 277, add new `submit` method)
- Modify: `app/bot/workflow_test.go` (add `stubAvailability` + new test)

- [ ] **Step 1: Add the `stubAvailability` helper to `workflow_test.go`**

Append to `app/bot/workflow_test.go` (the end of the existing helper block, e.g. right after `fakeIssueWorkflow`):

```go
// stubAvailability lets tests pre-program verdicts.
type stubAvailability struct {
	mu          sync.Mutex
	SoftVerdict queue.Verdict
	HardVerdict queue.Verdict
	SoftCalls   int
	HardCalls   int
}

func (s *stubAvailability) CheckSoft(ctx context.Context) queue.Verdict {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.SoftCalls++
	return s.SoftVerdict
}
func (s *stubAvailability) CheckHard(ctx context.Context) queue.Verdict {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.HardCalls++
	return s.HardVerdict
}
```

Also add `"sync"` to imports if not already present.

- [ ] **Step 2: Write the failing test**

Append to `app/bot/workflow_test.go`:

```go
func TestSubmit_NoWorkers_HardRejects(t *testing.T) {
	sl := &shimSlack{}
	avail := &stubAvailability{HardVerdict: queue.Verdict{Kind: queue.VerdictNoWorkers}}
	cfg := &config.Config{Channels: map[string]config.ChannelConfig{}}
	reg := workflow.NewRegistry()
	reg.Register(&fakeIssueWorkflow{})
	disp := workflow.NewDispatcher(reg, sl, nil)
	wf := NewWorkflow(cfg, disp, sl, nil, slog.Default(), avail)

	onSubmitCalled := false
	wf.SetSubmitHook(func(ctx context.Context, p *workflow.Pending) {
		onSubmitCalled = true
	})

	p := &workflow.Pending{ChannelID: "C1", ThreadTS: "T1", TaskType: "issue"}
	step := workflow.NextStep{Kind: workflow.NextStepSubmit, Pending: p}
	wf.executeStep(context.Background(), p, step, "")

	if avail.HardCalls != 1 {
		t.Errorf("HardCalls = %d, want 1", avail.HardCalls)
	}
	if onSubmitCalled {
		t.Error("onSubmit must NOT be called when verdict is NoWorkers")
	}

	foundReject := false
	for _, m := range sl.posted {
		if strings.Contains(m, ":x:") && strings.Contains(m, "沒有 worker") {
			foundReject = true
		}
	}
	if !foundReject {
		t.Errorf("expected :x: hard reject message; got posts: %+v", sl.posted)
	}
}
```

Also add `"strings"` to imports if not already present (and `slog` / `context` / `queue` which are already imported).

- [ ] **Step 3: Run — should fail (submit helper + reject not implemented)**

Run: `cd app && go test ./bot/ -run TestSubmit_NoWorkers_HardRejects -v`
Expected: FAIL — either `onSubmit` fires (no gate exists), or compilation fails because `w.submit` is undefined. Either failure mode is acceptable; Step 4 makes it pass.

- [ ] **Step 4: Add `submit()` helper and refactor `executeStep` to use it**

In `app/bot/workflow.go`, add a new method (place it right after `executeStep`):

```go
// submit is the single chokepoint for sending a Pending to the queue-submission
// closure. Consolidates the three former `if w.onSubmit != nil { w.onSubmit(...) }`
// call sites in executeStep so pre-submit checks (like the worker-availability
// hard check below) only need to land in one place.
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
			// Task 11 adds p.BusyHint = RenderBusyHint(verdict) here.
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

Then refactor the three `executeStep` call sites (they are inside the `NextStepOpenModal` case and the `NextStepSubmit` case; search for `w.onSubmit`). Replace each of the three `if w.onSubmit != nil { w.onSubmit(ctx, …) }` blocks with a single call:

Old (three occurrences, varying only in the variable name — `pending` or `p`):
```go
if w.onSubmit != nil {
    w.onSubmit(ctx, pending)
}
```

New:
```go
w.submit(ctx, pending)
```

For the `NextStepSubmit` case specifically, also remove the `else { w.logger.Warn("NextStepSubmit but no onSubmit hook set", ...) }` branch — the equivalent warn now lives inside `submit()`. The final case shape becomes:

```go
case workflow.NextStepSubmit:
	p := step.Pending
	if p == nil {
		p = pending
	}
	w.submit(ctx, p)
```

- [ ] **Step 5: Run — should pass**

Run: `cd app && go test ./bot/ -run TestSubmit_NoWorkers_HardRejects -v`
Expected: PASS.

- [ ] **Step 6: Run full bot tests (regression)**

Run: `cd app && go test ./bot/`
Expected: all PASS. In particular, the existing `TestExecuteStep_Submit_CallsHook` and `TestHandleSelection_DSelector_DispatchesWorkflow` must still pass — they do not set availability, so `submit()` passes straight through to `onSubmit` (the `if w.availability != nil` guard).

- [ ] **Step 7: Commit**

```bash
git add app/bot/workflow.go app/bot/workflow_test.go
git commit -m "$(cat <<'EOF'
feat(bot): add submit() helper, reject when no workers online

Collapses three executeStep→onSubmit call sites into a single submit()
chokepoint, and runs a hard availability check before dispatching.
NoWorkers verdict posts a reject message and clears thread dedup.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

### Task 11: `submit()` sets `Pending.BusyHint` on BusyEnqueueOK

**Files:**
- Modify: `app/bot/workflow.go` (the `submit()` method added in Task 10)
- Modify: `app/bot/workflow_test.go` (new test)

- [ ] **Step 1: Write the failing test**

Append to `app/bot/workflow_test.go`:

```go
func TestSubmit_BusyEnqueueOK_SetsBusyHint(t *testing.T) {
	sl := &shimSlack{}
	avail := &stubAvailability{
		HardVerdict: queue.Verdict{
			Kind:          queue.VerdictBusyEnqueueOK,
			EstimatedWait: 6 * time.Minute,
		},
	}
	cfg := &config.Config{Channels: map[string]config.ChannelConfig{}}
	reg := workflow.NewRegistry()
	reg.Register(&fakeIssueWorkflow{})
	disp := workflow.NewDispatcher(reg, sl, nil)
	wf := NewWorkflow(cfg, disp, sl, nil, slog.Default(), avail)

	var gotPending *workflow.Pending
	wf.SetSubmitHook(func(ctx context.Context, p *workflow.Pending) {
		gotPending = p
	})

	p := &workflow.Pending{ChannelID: "C1", ThreadTS: "T1", TaskType: "issue"}
	step := workflow.NextStep{Kind: workflow.NextStepSubmit, Pending: p}
	wf.executeStep(context.Background(), p, step, "")

	if gotPending == nil {
		t.Fatal("onSubmit was not called; expected BusyEnqueueOK to pass through")
	}
	if gotPending.BusyHint == "" {
		t.Errorf("BusyHint should be set; got empty")
	}
	if !strings.Contains(gotPending.BusyHint, "預估等候") {
		t.Errorf("BusyHint should contain 預估等候; got %q", gotPending.BusyHint)
	}
}
```

Add `"time"` import to workflow_test.go if not already present.

- [ ] **Step 2: Run — should fail**

Run: `cd app && go test ./bot/ -run TestSubmit_BusyEnqueueOK_SetsBusyHint -v`
Expected: FAIL — BusyHint is never set (Task 10 stubbed the branch with a TODO comment).

- [ ] **Step 3: Implement BusyHint assignment**

In `app/bot/workflow.go`, locate the `submit()` method added in Task 10. Replace the `case queue.VerdictBusyEnqueueOK` body (currently just a comment) with:

```go
		case queue.VerdictBusyEnqueueOK:
			p.BusyHint = RenderBusyHint(verdict)
```

- [ ] **Step 4: Run — should pass**

Run: `cd app && go test ./bot/ -run TestSubmit_BusyEnqueueOK -v`
Expected: PASS.

- [ ] **Step 5: Run full bot tests (regression)**

Run: `cd app && go test ./bot/`
Expected: all PASS.

- [ ] **Step 6: Commit**

```bash
git add app/bot/workflow.go app/bot/workflow_test.go
git commit -m "$(cat <<'EOF'
feat(bot): submit() sets Pending.BusyHint on BusyEnqueueOK

app.submitJob reads the hint and appends it to the lifecycle status
text (wired in Task 15).

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

### Task 12: Trigger-time soft warn in `HandleTrigger`

**Files:**
- Modify: `app/bot/workflow.go` (`HandleTrigger` ~line 83)
- Modify: `app/bot/workflow_test.go` (new test)

- [ ] **Step 1: Write the failing test**

Append to `app/bot/workflow_test.go`:

```go
func TestHandleTrigger_NoWorkers_PostsSoftWarnButContinues(t *testing.T) {
	sl := &shimSlack{}
	avail := &stubAvailability{SoftVerdict: queue.Verdict{Kind: queue.VerdictNoWorkers}}
	cfg := &config.Config{
		Channels: map[string]config.ChannelConfig{"C1": {}},
	}
	reg := workflow.NewRegistry()
	reg.Register(&fakeIssueWorkflow{})
	disp := workflow.NewDispatcher(reg, sl, nil)
	wf := NewWorkflow(cfg, disp, sl, nil, slog.Default(), avail)

	onSubmitCalled := false
	wf.SetSubmitHook(func(ctx context.Context, p *workflow.Pending) {
		onSubmitCalled = true
	})

	wf.HandleTrigger(slackclient.TriggerEvent{
		ChannelID: "C1",
		ThreadTS:  "T1",
		TriggerTS: "T1",
		UserID:    "U1",
		Text:      "issue foo/bar",
	})

	if avail.SoftCalls != 1 {
		t.Errorf("SoftCalls = %d, want 1", avail.SoftCalls)
	}

	foundWarn := false
	for _, m := range sl.posted {
		if strings.Contains(m, ":warning:") && strings.Contains(m, "沒有 worker") {
			foundWarn = true
		}
	}
	if !foundWarn {
		t.Errorf("expected :warning: soft warn; got posts: %+v", sl.posted)
	}

	// fakeIssueWorkflow.Trigger returns NextStepSubmit → hard check runs.
	// With nil hard verdict (zero value), submit() treats it as OK branch? No —
	// zero-value Kind "" is none of the switch cases, so submit falls through
	// to onSubmit. Thus soft warn must NOT short-circuit dispatch.
	if !onSubmitCalled {
		t.Error("soft warn must not block dispatch; expected onSubmit to still be called")
	}
}

func TestHandleTrigger_HealthyOK_NoSoftWarn(t *testing.T) {
	sl := &shimSlack{}
	avail := &stubAvailability{SoftVerdict: queue.Verdict{Kind: queue.VerdictOK}}
	cfg := &config.Config{
		Channels: map[string]config.ChannelConfig{"C1": {}},
	}
	reg := workflow.NewRegistry()
	reg.Register(&fakeIssueWorkflow{})
	disp := workflow.NewDispatcher(reg, sl, nil)
	wf := NewWorkflow(cfg, disp, sl, nil, slog.Default(), avail)

	wf.SetSubmitHook(func(ctx context.Context, p *workflow.Pending) {})

	wf.HandleTrigger(slackclient.TriggerEvent{
		ChannelID: "C1", ThreadTS: "T1", TriggerTS: "T1", UserID: "U1", Text: "issue foo/bar",
	})

	for _, m := range sl.posted {
		if strings.Contains(m, "沒有 worker") {
			t.Errorf("OK verdict should not post soft warn; got %q", m)
		}
	}
}
```

Note about the `TestHandleTrigger_NoWorkers_PostsSoftWarnButContinues` test: the fake issue workflow returns `NextStepSubmit` directly, so the flow reaches `submit()`. With the stub's **hard** verdict zero-valued (default `VerdictKind("")`), the `switch` falls through to `onSubmit`. That's the assertion that soft warn doesn't short-circuit. If future refactoring makes zero-value verdicts reject, this test's hard verdict needs to be explicitly set to `VerdictOK`.

- [ ] **Step 2: Run — should fail**

Run: `cd app && go test ./bot/ -run TestHandleTrigger_NoWorkers_PostsSoftWarn -v`
Expected: FAIL — `avail.SoftCalls` stays at 0 because `HandleTrigger` doesn't call `CheckSoft` yet.

- [ ] **Step 3: Insert the soft check in `HandleTrigger`**

In `app/bot/workflow.go`, locate `HandleTrigger` (~line 83). After the channel-binding check (the `if _, ok := w.cfg.Channels[...]` block ending with the `}`), and BEFORE `ctx := context.Background()` that precedes `dispatcher.Dispatch`, insert:

```go
	// Soft availability check — informational only; do NOT block dispatch.
	// The hard check inside submit() gates actual queue submission.
	if w.availability != nil {
		verdict := w.availability.CheckSoft(context.Background())
		if verdict.Kind == queue.VerdictNoWorkers {
			_ = w.slack.PostMessage(event.ChannelID,
				RenderSoftWarn(verdict), event.ThreadTS)
		}
	}
```

- [ ] **Step 4: Run — should pass**

Run: `cd app && go test ./bot/ -run TestHandleTrigger -v`
Expected: PASS for the new tests AND the existing `TestHandleTrigger_NoThread_PostsWarning` / `TestHandleTrigger_UnboundChannel_Silent`.

- [ ] **Step 5: Run full bot tests (regression)**

Run: `cd app && go test ./bot/`
Expected: all PASS.

- [ ] **Step 6: Commit**

```bash
git add app/bot/workflow.go app/bot/workflow_test.go
git commit -m "$(cat <<'EOF'
feat(bot): HandleTrigger posts soft warn when no workers (non-blocking)

Dispatch still proceeds; the hard check inside submit() gates the
actual queue submission.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Phase 6 — Worker Side

### Task 13: Worker registers with `Slots: 1`

**Files:**
- Modify: `worker/pool/pool.go` (`workerHeartbeat`, around line 241)

- [ ] **Step 1: Update both call sites in `workerHeartbeat`**

In `worker/pool/pool.go`, the function `workerHeartbeat` (around line 241) builds `WorkerInfo` in two places. Add `Slots: 1,` to both:

(a) Initial registration (around line 244):

```go
	for i := 0; i < p.cfg.WorkerCount; i++ {
		info := queue.WorkerInfo{
			WorkerID:    fmt.Sprintf("%s/worker-%d", p.cfg.Hostname, i),
			Name:        p.cfg.Hostname,
			Nickname:    p.nicknameForIndex(i),
			Slots:       1, // hardcoded; future work: read from worker.yaml when concurrent execution lands
			ConnectedAt: now,
		}
		// ...
	}
```

(b) Ticker re-registration (around line 262):

```go
			for i := 0; i < p.cfg.WorkerCount; i++ {
				info := queue.WorkerInfo{
					WorkerID:    fmt.Sprintf("%s/worker-%d", p.cfg.Hostname, i),
					Name:        p.cfg.Hostname,
					Nickname:    p.nicknameForIndex(i),
					Slots:       1, // see initial-registration comment above
					ConnectedAt: now,
				}
				p.cfg.Queue.Register(ctx, info)
			}
```

- [ ] **Step 2: Verify worker compiles & tests pass**

Run: `cd worker && go test ./...`
Expected: all PASS.

- [ ] **Step 3: Commit**

```bash
git add worker/pool/pool.go
git commit -m "$(cat <<'EOF'
feat(worker): declare Slots: 1 in heartbeat WorkerInfo

Hardcoded — matches today's single-job-per-worker pool. When the pool
gains concurrent execution, this lifts to worker.yaml config.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Phase 7 — App Wiring + Config

### Task 14: Add `AvailabilityConfig` to `app/config`

**Files:**
- Modify: `app/config/config.go`
- Modify: `app/config/defaults.go`

- [ ] **Step 1: Add `AvailabilityConfig` type + `Availability` field on `Config`**

In `app/config/config.go`:

(a) Append the new type after the `QueueConfig` declaration (around line 132):

```go
type AvailabilityConfig struct {
	AvgJobDuration time.Duration `yaml:"avg_job_duration"`
}
```

(b) Add the field to the top-level `Config` struct (after `Queue QueueConfig`, around line 27):

```go
type Config struct {
	// ... existing fields ...
	Queue        QueueConfig        `yaml:"queue"`
	Availability AvailabilityConfig `yaml:"availability"` // NEW
	Logging      LoggingConfig      `yaml:"logging"`
	// ... rest ...
}
```

- [ ] **Step 2: Apply default in `defaults.go`**

In `app/config/defaults.go`, add inside `ApplyDefaults` (e.g. after the queue defaults block, around line 61):

```go
	if cfg.Availability.AvgJobDuration <= 0 {
		cfg.Availability.AvgJobDuration = 3 * time.Minute
	}
```

- [ ] **Step 3: Verify config tests pass**

Run: `cd app && go test ./config/ -v`
Expected: all PASS. `DefaultsMap` round-trips the new field.

- [ ] **Step 4: Commit**

```bash
git add app/config/config.go app/config/defaults.go
git commit -m "$(cat <<'EOF'
feat(app/config): add availability.avg_job_duration (default 3m)

Optional field; powers ETA calculation in the worker availability
service. Absent → 3m default applied via ApplyDefaults.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

### Task 15: Wire availability into `app/app.go` + append BusyHint in `submitJob`

**Files:**
- Modify: `app/app.go` (construct availability before `bot.NewWorkflow` ~line 170; append `BusyHint` in `submitJob` closure after `BuildJob` ~line 196)

- [ ] **Step 1: Construct availability and pass to `NewWorkflow`**

In `app/app.go`, immediately after `dispatcher := workflow.NewDispatcher(reg, slackPort, appLogger)` (around line 168) and BEFORE `wf := bot.NewWorkflow(...)` (around line 170), insert:

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
```

Then update the `NewWorkflow` call (around line 170) — replace the trailing `nil` (added in Task 9) with `availability`:

```go
	wf := bot.NewWorkflow(cfg, dispatcher, slackPort, repoDiscovery, appLogger, availability)
```

- [ ] **Step 2: Append `BusyHint` to `statusText` in `submitJob`**

In `app/app.go`, locate the `submitJob` closure. Find the block (around line 196):

```go
		job, statusText, err := wfImpl.BuildJob(ctx, p)
		if err != nil {
			appLogger.Error("BuildJob failed", "phase", "失敗", "error", err)
			_ = slackPort.PostMessage(p.ChannelID, fmt.Sprintf(":x: %v", err), p.ThreadTS)
			if handler != nil {
				handler.ClearThreadDedup(p.ChannelID, p.ThreadTS)
			}
			return
		}

		// Post lifecycle status message.
		statusMsgTS, postErr := slackPort.PostMessageWithTS(p.ChannelID, statusText, p.ThreadTS)
```

Insert the append right after the error-return block and before `statusMsgTS` is posted:

```go
		job, statusText, err := wfImpl.BuildJob(ctx, p)
		if err != nil {
			appLogger.Error("BuildJob failed", "phase", "失敗", "error", err)
			_ = slackPort.PostMessage(p.ChannelID, fmt.Sprintf(":x: %v", err), p.ThreadTS)
			if handler != nil {
				handler.ClearThreadDedup(p.ChannelID, p.ThreadTS)
			}
			return
		}

		// Append worker-availability busy hint if the pre-submit check set one.
		if p.BusyHint != "" {
			statusText += " " + p.BusyHint
		}

		// Post lifecycle status message.
		statusMsgTS, postErr := slackPort.PostMessageWithTS(p.ChannelID, statusText, p.ThreadTS)
```

- [ ] **Step 3: Verify the whole tree compiles**

Run: `go build ./...`
Expected: success.

- [ ] **Step 4: Run all tests (regression)**

Run: `go test ./...`
Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add app/app.go
git commit -m "$(cat <<'EOF'
feat(app): construct WorkerAvailability + append BusyHint in submitJob

Wires verdict/dep-error hooks into Prometheus and injects availability
into bot.Workflow. In submitJob, reads Pending.BusyHint (set by the
shim's submit() helper) and appends it to the lifecycle status text
before posting. Two-stage precheck is now end-to-end active.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Phase 8 — Final Verification

### Task 16: Full build + import direction + test suite

**Files:** none (verification only)

- [ ] **Step 1: Build everything**

Run: `go build ./...`
Expected: success across all three modules (app, worker, shared, root).

- [ ] **Step 2: Run the full test suite**

Run: `go test ./...`
Expected: all PASS, including:
- `shared/queue/...` (new availability tests)
- `app/bot/...` (new workflow integration tests)
- `app/config/...` (config defaults round-trip)
- `worker/pool/...` (existing; Slots field is additive)
- `test/import_direction_test.go` (no new cross-module imports introduced)

- [ ] **Step 3: Verify import direction explicitly**

Run: `go test ./test/ -run TestImportDirection -v`
Expected: PASS. The new code respects boundaries:
- `shared/queue` does NOT import `shared/metrics` (uses functional-option hooks).
- `app/bot/verdict_message.go` imports `shared/queue` (allowed).
- `app/app.go` imports `shared/metrics` and `shared/queue` (allowed).

- [ ] **Step 4: Confirm no committed `nil` placeholder remains**

Run: `grep -n "NewWorkflow.*nil" app/app.go`
Expected: no matches (Task 15 replaced the `nil` from Task 9).

- [ ] **Step 5: Manually exercise (optional, off-CI)**

Optionally bring up Redis + start an `agentdock app` with no workers and confirm:
- `@bot` in a thread → soft warn appears, repo selector still appears.
- After completing selection → hard reject appears, `:mag:` lifecycle message does NOT.
- `agentdock worker` started → next `@bot` works normally with no warn.

This is documentation of the manual smoke; the integration tests are the gate.

- [ ] **Step 6: No commit needed**

If all of the above pass, the implementation is complete. The plan was committed-as-you-go; no final commit step.

---

## Spec Coverage Self-Review

Cross-referenced against `docs/superpowers/specs/2026-04-22-worker-liveness-precheck-design.md`:

| Spec Section | Implementing Task(s) |
|---|---|
| §1 Data Model — `WorkerInfo.Slots` | Task 1 |
| §2 Availability Service — types, interface, compute | Tasks 2–4 |
| §2 Slots normalisation | Task 5 (locked by test) |
| §2 Fail-open | Task 6 |
| §3.1 Workflow struct + NewWorkflow constructor change | Task 9 |
| §3.1 `workflow.Pending.BusyHint` field | Task 9 |
| §3.2 Trigger-time soft warn in HandleTrigger | Task 12 |
| §3.3 `submit()` helper + NoWorkers hard reject | Task 10 |
| §3.3 BusyEnqueueOK sets `Pending.BusyHint` | Task 11 |
| §3.3 `submitJob` appends BusyHint to statusText | Task 15 |
| §4 Verdict rendering | Task 8 |
| §5 Wiring + config | Tasks 14, 15 |
| §6 Worker Slots: 1 | Task 13 |
| §7 Metrics (verdict, duration, dep errors) | Tasks 7, 15 |
| §8 Error Handling Summary | Tasks 6 (deps), 10 (Slack reject), 12 (Slack warn) |
| §9 Ordering — soft AFTER channel-guard | Task 12 (insertion location specified) |
| §9 Ordering — hard check BEFORE onSubmit | Task 10 (enforced by `submit()` helper) |
| §9 Ordering — hard reject calls ClearThreadDedup | Task 10 (impl explicit; test asserts onSubmit not called) |
| §9 Ordering — BusyHint append after BuildJob, before PostMessageWithTS | Task 15 |
| Testing — unit matrix (9 cases) | Tasks 2–6 cover all 9 rows |
| Testing — 3 integration cases | Tasks 10, 11, 12 |
| Migration / Rollout | Additive changes in Tasks 1, 14 (omitempty + optional config) |
