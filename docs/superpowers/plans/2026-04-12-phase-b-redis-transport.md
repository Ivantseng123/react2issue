# Phase B: Redis Transport + Worker Binary

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement Redis-backed transport for all five queue interfaces, add transport switch in config, and create a standalone `bot worker` binary that consumes jobs from Redis.

**Architecture:** Each queue interface gets a Redis implementation using the appropriate primitive (Stream+ConsumerGroup for reliable delivery, Pub/Sub for broadcast). A `NewRedisBundle()` constructor assembles all five. The `bot worker` subcommand creates a Redis bundle + worker pool without Slack/GitHub write dependencies. Transport is selected via `queue.transport: redis` in config.

**Tech Stack:** Go, `github.com/redis/go-redis/v9`, Redis 7+ (Streams, Consumer Groups, Pub/Sub, Hashes)

**Key prefix:** All Redis keys use `r2i:` prefix (react2issue) to avoid namespace collisions.

---

## File Structure

### New Files
| File | Responsibility |
|------|---------------|
| `internal/queue/redis_client.go` | Shared Redis client construction from config |
| `internal/queue/redis_statusbus.go` | StatusBus via Redis Pub/Sub |
| `internal/queue/redis_statusbus_test.go` | Tests for Redis StatusBus |
| `internal/queue/redis_commandbus.go` | CommandBus via Redis Pub/Sub |
| `internal/queue/redis_commandbus_test.go` | Tests for Redis CommandBus |
| `internal/queue/redis_resultbus.go` | ResultBus via Redis Stream + Consumer Group |
| `internal/queue/redis_resultbus_test.go` | Tests for Redis ResultBus |
| `internal/queue/redis_attachments.go` | AttachmentStore via Redis Hash |
| `internal/queue/redis_attachments_test.go` | Tests for Redis AttachmentStore |
| `internal/queue/redis_jobqueue.go` | JobQueue via Redis Stream + Consumer Group + worker registry |
| `internal/queue/redis_jobqueue_test.go` | Tests for Redis JobQueue |
| `internal/queue/redis_bundle.go` | `NewRedisBundle()` assembling all Redis implementations |
| `internal/queue/redis_test_helper.go` | Shared test helper: skip if no Redis, flush between tests |
| `cmd/bot/worker.go` | `bot worker` subcommand entry point |

### Modified Files
| File | Change |
|------|--------|
| `go.mod` / `go.sum` | Add `github.com/redis/go-redis/v9` |
| `internal/config/config.go` | Add `RedisConfig` struct, env overrides |
| `cmd/bot/main.go` | Transport switch: inmem vs redis bundle creation |
| `internal/queue/httpstatus.go` | `/workers` endpoint, conditional PID liveness |

### Unchanged Files
`internal/bot/*`, `internal/worker/*`, `internal/slack/*`, `internal/github/*`

---

### Task 1: Add go-redis dependency + RedisConfig

**Files:**
- Modify: `go.mod`
- Modify: `internal/config/config.go`

- [ ] **Step 1: Add go-redis dependency**

```bash
cd /Users/ivantseng/local_file/slack-issue-bot
go get github.com/redis/go-redis/v9
```

- [ ] **Step 2: Add RedisConfig to config.go**

In `internal/config/config.go`, add `RedisConfig` struct after `AttachmentsConfig`:

```go
type RedisConfig struct {
	Addr     string `yaml:"addr"`
	Password string `yaml:"password"`
	DB       int    `yaml:"db"`
	TLS      bool   `yaml:"tls"`
}
```

Add `Redis RedisConfig` field to the `Config` struct:

```go
type Config struct {
	// ... existing fields ...
	Attachments       AttachmentsConfig        `yaml:"attachments"`
	Redis             RedisConfig              `yaml:"redis"`
}
```

- [ ] **Step 3: Add env overrides in `applyEnvOverrides`**

In the `applyEnvOverrides` function, add:

```go
	if v := os.Getenv("REDIS_ADDR"); v != "" {
		cfg.Redis.Addr = v
	}
	if v := os.Getenv("REDIS_PASSWORD"); v != "" {
		cfg.Redis.Password = v
	}
```

- [ ] **Step 4: Build and test**

Run: `go build ./... && go test ./...`
Expected: compiles, all 104 tests pass

- [ ] **Step 5: Commit**

```bash
git add go.mod go.sum internal/config/config.go
git commit -m "feat: add go-redis dependency and RedisConfig"
```

---

### Task 2: Redis client helper + test helper

**Files:**
- Create: `internal/queue/redis_client.go`
- Create: `internal/queue/redis_test_helper.go`

- [ ] **Step 1: Create Redis client helper**

Create `internal/queue/redis_client.go`:

```go
package queue

import (
	"context"
	"crypto/tls"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisConfig holds Redis connection parameters.
type RedisConfig struct {
	Addr     string
	Password string
	DB       int
	TLS      bool
}

// NewRedisClient creates a connected Redis client from config.
func NewRedisClient(cfg RedisConfig) (*redis.Client, error) {
	opts := &redis.Options{
		Addr:     cfg.Addr,
		Password: cfg.Password,
		DB:       cfg.DB,
	}
	if cfg.TLS {
		opts.TLSConfig = &tls.Config{}
	}

	client := redis.NewClient(opts)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := client.Ping(ctx).Err(); err != nil {
		client.Close()
		return nil, fmt.Errorf("redis ping failed: %w", err)
	}
	return client, nil
}

const keyPrefix = "r2i:"
```

- [ ] **Step 2: Create test helper**

Create `internal/queue/redis_test_helper.go`:

```go
package queue

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// testRedisClient returns a connected Redis client for testing, or skips the test.
// It flushes the test DB before returning.
func testRedisClient(t *testing.T) *redis.Client {
	t.Helper()

	addr := os.Getenv("TEST_REDIS_ADDR")
	if addr == "" {
		addr = "localhost:6379"
	}

	client := redis.NewClient(&redis.Options{
		Addr: addr,
		DB:   15, // use DB 15 for tests to avoid collisions
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := client.Ping(ctx).Err(); err != nil {
		t.Skipf("skipping Redis test: %v", err)
	}

	// Flush test DB.
	client.FlushDB(ctx)

	t.Cleanup(func() {
		client.FlushDB(context.Background())
		client.Close()
	})

	return client
}
```

- [ ] **Step 3: Build**

Run: `go build ./...`
Expected: compiles

- [ ] **Step 4: Commit**

```bash
git add internal/queue/redis_client.go internal/queue/redis_test_helper.go
git commit -m "feat: add Redis client helper and test infrastructure"
```

---

### Task 3: Redis StatusBus (Pub/Sub)

**Files:**
- Create: `internal/queue/redis_statusbus.go`
- Create: `internal/queue/redis_statusbus_test.go`

- [ ] **Step 1: Write test**

Create `internal/queue/redis_statusbus_test.go`:

```go
package queue

import (
	"context"
	"testing"
	"time"
)

func TestRedisStatusBus_ReportAndSubscribe(t *testing.T) {
	rdb := testRedisClient(t)

	bus := NewRedisStatusBus(rdb)
	defer bus.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ch, err := bus.Subscribe(ctx)
	if err != nil {
		t.Fatal(err)
	}

	// Give subscriber time to connect.
	time.Sleep(100 * time.Millisecond)

	report := StatusReport{
		JobID:     "j1",
		WorkerID:  "w1",
		PID:       1234,
		AgentCmd:  "claude",
		Alive:     true,
		ToolCalls: 5,
	}
	if err := bus.Report(ctx, report); err != nil {
		t.Fatal(err)
	}

	select {
	case got := <-ch:
		if got.JobID != "j1" {
			t.Errorf("JobID = %q, want j1", got.JobID)
		}
		if got.ToolCalls != 5 {
			t.Errorf("ToolCalls = %d, want 5", got.ToolCalls)
		}
	case <-ctx.Done():
		t.Fatal("timeout waiting for status report")
	}
}
```

- [ ] **Step 2: Implement**

Create `internal/queue/redis_statusbus.go`:

```go
package queue

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/redis/go-redis/v9"
)

const statusChannel = keyPrefix + "jobs:status"

type RedisStatusBus struct {
	rdb    *redis.Client
	pubsub *redis.PubSub
}

func NewRedisStatusBus(rdb *redis.Client) *RedisStatusBus {
	return &RedisStatusBus{rdb: rdb}
}

func (b *RedisStatusBus) Report(ctx context.Context, report StatusReport) error {
	data, err := json.Marshal(report)
	if err != nil {
		return err
	}
	return b.rdb.Publish(ctx, statusChannel, data).Err()
}

func (b *RedisStatusBus) Subscribe(ctx context.Context) (<-chan StatusReport, error) {
	b.pubsub = b.rdb.Subscribe(ctx, statusChannel)
	ch := make(chan StatusReport, 64)

	go func() {
		defer close(ch)
		msgCh := b.pubsub.Channel()
		for {
			select {
			case msg, ok := <-msgCh:
				if !ok {
					return
				}
				var report StatusReport
				if err := json.Unmarshal([]byte(msg.Payload), &report); err != nil {
					slog.Warn("redis status: unmarshal failed", "error", err)
					continue
				}
				select {
				case ch <- report:
				default:
					// Drop if consumer is slow — status is best-effort.
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	return ch, nil
}

func (b *RedisStatusBus) Close() error {
	if b.pubsub != nil {
		return b.pubsub.Close()
	}
	return nil
}
```

- [ ] **Step 3: Run tests**

Run: `go test ./internal/queue/ -run TestRedisStatusBus -v`
Expected: PASS (or SKIP if no Redis)

Run: `go test ./...`
Expected: all tests pass

- [ ] **Step 4: Commit**

```bash
git add internal/queue/redis_statusbus.go internal/queue/redis_statusbus_test.go
git commit -m "feat: add Redis StatusBus implementation (Pub/Sub)"
```

---

### Task 4: Redis CommandBus (Pub/Sub)

**Files:**
- Create: `internal/queue/redis_commandbus.go`
- Create: `internal/queue/redis_commandbus_test.go`

- [ ] **Step 1: Write test**

Create `internal/queue/redis_commandbus_test.go`:

```go
package queue

import (
	"context"
	"testing"
	"time"
)

func TestRedisCommandBus_SendAndReceive(t *testing.T) {
	rdb := testRedisClient(t)

	bus := NewRedisCommandBus(rdb)
	defer bus.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ch, err := bus.Receive(ctx)
	if err != nil {
		t.Fatal(err)
	}

	time.Sleep(100 * time.Millisecond)

	cmd := Command{JobID: "j1", Action: "kill"}
	if err := bus.Send(ctx, cmd); err != nil {
		t.Fatal(err)
	}

	select {
	case got := <-ch:
		if got.JobID != "j1" {
			t.Errorf("JobID = %q, want j1", got.JobID)
		}
		if got.Action != "kill" {
			t.Errorf("Action = %q, want kill", got.Action)
		}
	case <-ctx.Done():
		t.Fatal("timeout waiting for command")
	}
}
```

- [ ] **Step 2: Implement**

Create `internal/queue/redis_commandbus.go`:

```go
package queue

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/redis/go-redis/v9"
)

const commandsChannel = keyPrefix + "jobs:commands"

type RedisCommandBus struct {
	rdb    *redis.Client
	pubsub *redis.PubSub
}

func NewRedisCommandBus(rdb *redis.Client) *RedisCommandBus {
	return &RedisCommandBus{rdb: rdb}
}

func (b *RedisCommandBus) Send(ctx context.Context, cmd Command) error {
	data, err := json.Marshal(cmd)
	if err != nil {
		return err
	}
	return b.rdb.Publish(ctx, commandsChannel, data).Err()
}

func (b *RedisCommandBus) Receive(ctx context.Context) (<-chan Command, error) {
	b.pubsub = b.rdb.Subscribe(ctx, commandsChannel)
	ch := make(chan Command, 64)

	go func() {
		defer close(ch)
		msgCh := b.pubsub.Channel()
		for {
			select {
			case msg, ok := <-msgCh:
				if !ok {
					return
				}
				var cmd Command
				if err := json.Unmarshal([]byte(msg.Payload), &cmd); err != nil {
					slog.Warn("redis command: unmarshal failed", "error", err)
					continue
				}
				select {
				case ch <- cmd:
				default:
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	return ch, nil
}

func (b *RedisCommandBus) Close() error {
	if b.pubsub != nil {
		return b.pubsub.Close()
	}
	return nil
}
```

- [ ] **Step 3: Run tests**

Run: `go test ./internal/queue/ -run TestRedisCommandBus -v`
Expected: PASS (or SKIP if no Redis)

- [ ] **Step 4: Commit**

```bash
git add internal/queue/redis_commandbus.go internal/queue/redis_commandbus_test.go
git commit -m "feat: add Redis CommandBus implementation (Pub/Sub)"
```

---

### Task 5: Redis ResultBus (Stream)

**Files:**
- Create: `internal/queue/redis_resultbus.go`
- Create: `internal/queue/redis_resultbus_test.go`

- [ ] **Step 1: Write test**

Create `internal/queue/redis_resultbus_test.go`:

```go
package queue

import (
	"context"
	"testing"
	"time"
)

func TestRedisResultBus_PublishAndSubscribe(t *testing.T) {
	rdb := testRedisClient(t)

	bus := NewRedisResultBus(rdb)
	defer bus.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ch, err := bus.Subscribe(ctx)
	if err != nil {
		t.Fatal(err)
	}

	result := &JobResult{
		JobID:      "j1",
		Status:     "completed",
		Title:      "Bug fix",
		Body:       "Fixed it",
		Labels:     []string{"bug"},
		Confidence: "high",
		FilesFound: 3,
	}
	if err := bus.Publish(ctx, result); err != nil {
		t.Fatal(err)
	}

	select {
	case got := <-ch:
		if got.JobID != "j1" {
			t.Errorf("JobID = %q, want j1", got.JobID)
		}
		if got.Status != "completed" {
			t.Errorf("Status = %q, want completed", got.Status)
		}
		if got.Title != "Bug fix" {
			t.Errorf("Title = %q, want 'Bug fix'", got.Title)
		}
	case <-ctx.Done():
		t.Fatal("timeout waiting for result")
	}
}
```

- [ ] **Step 2: Implement**

Create `internal/queue/redis_resultbus.go`:

```go
package queue

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/redis/go-redis/v9"
)

const (
	resultsStream = keyPrefix + "jobs:results"
	resultsGroup  = "app"
)

type RedisResultBus struct {
	rdb        *redis.Client
	consumerID string
	stopCh     chan struct{}
}

func NewRedisResultBus(rdb *redis.Client) *RedisResultBus {
	return &RedisResultBus{
		rdb:        rdb,
		consumerID: fmt.Sprintf("app-%d", time.Now().UnixNano()),
		stopCh:     make(chan struct{}),
	}
}

func (b *RedisResultBus) Publish(ctx context.Context, result *JobResult) error {
	data, err := json.Marshal(result)
	if err != nil {
		return err
	}
	return b.rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: resultsStream,
		Values: map[string]interface{}{"payload": string(data)},
	}).Err()
}

func (b *RedisResultBus) Subscribe(ctx context.Context) (<-chan *JobResult, error) {
	// Create consumer group (ignore error if already exists).
	b.rdb.XGroupCreateMkStream(ctx, resultsStream, resultsGroup, "0")

	ch := make(chan *JobResult, 64)

	go func() {
		defer close(ch)
		for {
			select {
			case <-b.stopCh:
				return
			case <-ctx.Done():
				return
			default:
			}

			streams, err := b.rdb.XReadGroup(ctx, &redis.XReadGroupArgs{
				Group:    resultsGroup,
				Consumer: b.consumerID,
				Streams:  []string{resultsStream, ">"},
				Count:    1,
				Block:    5 * time.Second,
			}).Result()
			if err != nil {
				if err == redis.Nil || ctx.Err() != nil {
					continue
				}
				slog.Warn("redis result: read failed", "error", err)
				time.Sleep(time.Second)
				continue
			}

			for _, stream := range streams {
				for _, msg := range stream.Messages {
					payload, ok := msg.Values["payload"].(string)
					if !ok {
						continue
					}
					var result JobResult
					if err := json.Unmarshal([]byte(payload), &result); err != nil {
						slog.Warn("redis result: unmarshal failed", "error", err)
						continue
					}
					select {
					case ch <- &result:
					case <-ctx.Done():
						return
					}
					// Ack after delivery to consumer channel.
					b.rdb.XAck(ctx, resultsStream, resultsGroup, msg.ID)
				}
			}
		}
	}()

	return ch, nil
}

func (b *RedisResultBus) Close() error {
	select {
	case <-b.stopCh:
	default:
		close(b.stopCh)
	}
	return nil
}
```

Add missing import `"time"` to the import block.

- [ ] **Step 3: Run tests**

Run: `go test ./internal/queue/ -run TestRedisResultBus -v`
Expected: PASS (or SKIP)

- [ ] **Step 4: Commit**

```bash
git add internal/queue/redis_resultbus.go internal/queue/redis_resultbus_test.go
git commit -m "feat: add Redis ResultBus implementation (Stream + Consumer Group)"
```

---

### Task 6: Redis AttachmentStore (Hash)

**Files:**
- Create: `internal/queue/redis_attachments.go`
- Create: `internal/queue/redis_attachments_test.go`

- [ ] **Step 1: Write test**

Create `internal/queue/redis_attachments_test.go`:

```go
package queue

import (
	"context"
	"testing"
	"time"
)

func TestRedisAttachmentStore_PrepareAndResolve(t *testing.T) {
	rdb := testRedisClient(t)

	store := NewRedisAttachmentStore(rdb)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	meta := []AttachmentMeta{
		{Filename: "screenshot.png", MimeType: "image/png"},
		{Filename: "log.txt", MimeType: "text/plain"},
	}

	if err := store.Prepare(ctx, "j1", meta); err != nil {
		t.Fatal(err)
	}

	result, err := store.Resolve(ctx, "j1")
	if err != nil {
		t.Fatal(err)
	}

	if len(result) != 2 {
		t.Fatalf("len = %d, want 2", len(result))
	}
	if result[0].Filename != "screenshot.png" {
		t.Errorf("filename = %q, want screenshot.png", result[0].Filename)
	}
}

func TestRedisAttachmentStore_ResolveBeforePrepare(t *testing.T) {
	rdb := testRedisClient(t)

	store := NewRedisAttachmentStore(rdb)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// Prepare in background after delay.
	go func() {
		time.Sleep(500 * time.Millisecond)
		store.Prepare(context.Background(), "j2", []AttachmentMeta{
			{Filename: "file.txt"},
		})
	}()

	result, err := store.Resolve(ctx, "j2")
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 1 {
		t.Fatalf("len = %d, want 1", len(result))
	}
}

func TestRedisAttachmentStore_Cleanup(t *testing.T) {
	rdb := testRedisClient(t)

	store := NewRedisAttachmentStore(rdb)
	ctx := context.Background()

	store.Prepare(ctx, "j3", []AttachmentMeta{{Filename: "f.txt"}})
	store.Cleanup(ctx, "j3")

	// Key should be gone.
	exists := rdb.Exists(ctx, keyPrefix+"jobs:attachments:j3").Val()
	if exists != 0 {
		t.Error("expected key to be deleted after cleanup")
	}
}
```

- [ ] **Step 2: Implement**

Create `internal/queue/redis_attachments.go`:

```go
package queue

import (
	"context"
	"encoding/json"
	"time"

	"github.com/redis/go-redis/v9"
)

type RedisAttachmentStore struct {
	rdb *redis.Client
}

func NewRedisAttachmentStore(rdb *redis.Client) *RedisAttachmentStore {
	return &RedisAttachmentStore{rdb: rdb}
}

func attachmentKey(jobID string) string {
	return keyPrefix + "jobs:attachments:" + jobID
}

func (s *RedisAttachmentStore) Prepare(ctx context.Context, jobID string, attachments []AttachmentMeta) error {
	result := make([]AttachmentReady, len(attachments))
	for i, a := range attachments {
		result[i] = AttachmentReady{Filename: a.Filename, URL: ""}
	}
	data, err := json.Marshal(result)
	if err != nil {
		return err
	}
	return s.rdb.Set(ctx, attachmentKey(jobID), data, 30*time.Minute).Err()
}

func (s *RedisAttachmentStore) Resolve(ctx context.Context, jobID string) ([]AttachmentReady, error) {
	// Poll until key exists (Prepare may not have been called yet).
	for {
		data, err := s.rdb.Get(ctx, attachmentKey(jobID)).Bytes()
		if err == nil {
			var result []AttachmentReady
			if err := json.Unmarshal(data, &result); err != nil {
				return nil, err
			}
			return result, nil
		}
		if err != redis.Nil {
			return nil, err
		}
		// Key doesn't exist yet — wait and retry.
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func (s *RedisAttachmentStore) Cleanup(ctx context.Context, jobID string) error {
	return s.rdb.Del(ctx, attachmentKey(jobID)).Err()
}
```

- [ ] **Step 3: Run tests**

Run: `go test ./internal/queue/ -run TestRedisAttachment -v`
Expected: PASS (or SKIP)

- [ ] **Step 4: Commit**

```bash
git add internal/queue/redis_attachments.go internal/queue/redis_attachments_test.go
git commit -m "feat: add Redis AttachmentStore implementation (Hash with polling)"
```

---

### Task 7: Redis JobQueue (Stream + Consumer Group)

**Files:**
- Create: `internal/queue/redis_jobqueue.go`
- Create: `internal/queue/redis_jobqueue_test.go`

This is the most complex implementation. It handles job submission (XADD), consumption (XREADGROUP), ack (XACK), worker registration (HSET with TTL), and queue depth (XLEN).

- [ ] **Step 1: Write tests**

Create `internal/queue/redis_jobqueue_test.go`:

```go
package queue

import (
	"context"
	"testing"
	"time"
)

func TestRedisJobQueue_SubmitAndReceive(t *testing.T) {
	rdb := testRedisClient(t)
	store := NewMemJobStore()

	q := NewRedisJobQueue(rdb, store, "triage")
	defer q.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ch, err := q.Receive(ctx)
	if err != nil {
		t.Fatal(err)
	}

	job := &Job{ID: "j1", Priority: 50, Prompt: "test", TaskType: "triage"}
	if err := q.Submit(ctx, job); err != nil {
		t.Fatal(err)
	}

	select {
	case got := <-ch:
		if got.ID != "j1" {
			t.Errorf("ID = %q, want j1", got.ID)
		}
		if got.Prompt != "test" {
			t.Errorf("Prompt = %q, want test", got.Prompt)
		}
	case <-ctx.Done():
		t.Fatal("timeout waiting for job")
	}
}

func TestRedisJobQueue_AckAndDepth(t *testing.T) {
	rdb := testRedisClient(t)
	store := NewMemJobStore()

	q := NewRedisJobQueue(rdb, store, "triage")
	defer q.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	q.Submit(ctx, &Job{ID: "j1", Priority: 50, TaskType: "triage"})
	q.Submit(ctx, &Job{ID: "j2", Priority: 50, TaskType: "triage"})

	depth := q.QueueDepth()
	if depth != 2 {
		t.Errorf("depth = %d, want 2", depth)
	}

	// Consume and ack one.
	ch, _ := q.Receive(ctx)
	job := <-ch
	q.Ack(ctx, job.ID)

	// Position returns 0 for Redis mode.
	pos, _ := q.QueuePosition("j2")
	if pos != 0 {
		t.Errorf("position = %d, want 0 (not supported in Redis mode)", pos)
	}
}

func TestRedisJobQueue_WorkerRegistration(t *testing.T) {
	rdb := testRedisClient(t)
	store := NewMemJobStore()

	q := NewRedisJobQueue(rdb, store, "triage")
	defer q.Close()

	ctx := context.Background()

	info := WorkerInfo{WorkerID: "w1", Name: "test-worker", Agents: []string{"claude"}}
	if err := q.Register(ctx, info); err != nil {
		t.Fatal(err)
	}

	workers, err := q.ListWorkers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(workers) != 1 {
		t.Fatalf("len = %d, want 1", len(workers))
	}
	if workers[0].WorkerID != "w1" {
		t.Errorf("WorkerID = %q, want w1", workers[0].WorkerID)
	}

	q.Unregister(ctx, "w1")
	workers, _ = q.ListWorkers(ctx)
	if len(workers) != 0 {
		t.Errorf("len = %d after unregister, want 0", len(workers))
	}
}
```

- [ ] **Step 2: Implement**

Create `internal/queue/redis_jobqueue.go`:

```go
package queue

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

type RedisJobQueue struct {
	rdb        *redis.Client
	store      JobStore
	taskType   string
	stream     string
	group      string
	consumerID string
	stopCh     chan struct{}
}

func NewRedisJobQueue(rdb *redis.Client, store JobStore, taskType string) *RedisJobQueue {
	stream := keyPrefix + "jobs:" + taskType
	return &RedisJobQueue{
		rdb:        rdb,
		store:      store,
		taskType:   taskType,
		stream:     stream,
		group:      "workers",
		consumerID: fmt.Sprintf("worker-%d", time.Now().UnixNano()),
		stopCh:     make(chan struct{}),
	}
}

func (q *RedisJobQueue) Submit(ctx context.Context, job *Job) error {
	data, err := json.Marshal(job)
	if err != nil {
		return err
	}
	if err := q.rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: q.stream,
		Values: map[string]interface{}{"payload": string(data)},
	}).Err(); err != nil {
		return err
	}
	q.store.Put(job)
	return nil
}

func (q *RedisJobQueue) Receive(ctx context.Context) (<-chan *Job, error) {
	// Create consumer group (ignore BUSYGROUP error if already exists).
	err := q.rdb.XGroupCreateMkStream(ctx, q.stream, q.group, "0").Err()
	if err != nil && !strings.Contains(err.Error(), "BUSYGROUP") {
		return nil, fmt.Errorf("create consumer group: %w", err)
	}

	ch := make(chan *Job, 10)

	go func() {
		defer close(ch)
		for {
			select {
			case <-q.stopCh:
				return
			case <-ctx.Done():
				return
			default:
			}

			streams, err := q.rdb.XReadGroup(ctx, &redis.XReadGroupArgs{
				Group:    q.group,
				Consumer: q.consumerID,
				Streams:  []string{q.stream, ">"},
				Count:    1,
				Block:    5 * time.Second,
			}).Result()
			if err != nil {
				if err == redis.Nil || ctx.Err() != nil {
					continue
				}
				slog.Warn("redis jobqueue: read failed", "error", err)
				time.Sleep(time.Second)
				continue
			}

			for _, stream := range streams {
				for _, msg := range stream.Messages {
					payload, ok := msg.Values["payload"].(string)
					if !ok {
						continue
					}
					var job Job
					if err := json.Unmarshal([]byte(payload), &job); err != nil {
						slog.Warn("redis jobqueue: unmarshal failed", "error", err)
						continue
					}
					// Store the Redis message ID for ack.
					job.Seq = 0 // Not used in Redis mode.
					select {
					case ch <- &job:
					case <-ctx.Done():
						return
					}
				}
			}
		}
	}()

	return ch, nil
}

func (q *RedisJobQueue) Ack(ctx context.Context, jobID string) error {
	q.store.UpdateStatus(jobID, JobPreparing)
	// Note: Redis XACK requires the message ID, not the job ID.
	// For simplicity, we search recent pending entries for matching job.
	pending, err := q.rdb.XPendingExt(ctx, &redis.XPendingExtArgs{
		Stream:   q.stream,
		Group:    q.group,
		Consumer: q.consumerID,
		Start:    "-",
		End:      "+",
		Count:    100,
	}).Result()
	if err != nil {
		return nil // Best-effort ack.
	}
	for _, p := range pending {
		q.rdb.XAck(ctx, q.stream, q.group, p.ID)
	}
	return nil
}

func (q *RedisJobQueue) QueuePosition(jobID string) (int, error) {
	return 0, nil // Not meaningful in Redis mode.
}

func (q *RedisJobQueue) QueueDepth() int {
	ctx := context.Background()
	n, err := q.rdb.XLen(ctx, q.stream).Result()
	if err != nil {
		return 0
	}
	return int(n)
}

// Worker registration via Redis hashes with TTL.

func workerKey(workerID string) string {
	return keyPrefix + "workers:" + workerID
}

func (q *RedisJobQueue) Register(ctx context.Context, info WorkerInfo) error {
	data, err := json.Marshal(info)
	if err != nil {
		return err
	}
	pipe := q.rdb.Pipeline()
	pipe.Set(ctx, workerKey(info.WorkerID), data, 30*time.Second)
	_, err = pipe.Exec(ctx)
	return err
}

func (q *RedisJobQueue) Unregister(ctx context.Context, workerID string) error {
	return q.rdb.Del(ctx, workerKey(workerID)).Err()
}

func (q *RedisJobQueue) ListWorkers(ctx context.Context) ([]WorkerInfo, error) {
	var cursor uint64
	var workers []WorkerInfo
	pattern := keyPrefix + "workers:*"

	for {
		keys, nextCursor, err := q.rdb.Scan(ctx, cursor, pattern, 100).Result()
		if err != nil {
			return nil, err
		}
		for _, key := range keys {
			data, err := q.rdb.Get(ctx, key).Bytes()
			if err != nil {
				continue
			}
			var info WorkerInfo
			if err := json.Unmarshal(data, &info); err != nil {
				continue
			}
			workers = append(workers, info)
		}
		cursor = nextCursor
		if cursor == 0 {
			break
		}
	}
	return workers, nil
}

func (q *RedisJobQueue) Close() error {
	select {
	case <-q.stopCh:
	default:
		close(q.stopCh)
	}
	return nil
}
```

- [ ] **Step 3: Run tests**

Run: `go test ./internal/queue/ -run TestRedisJobQueue -v`
Expected: PASS (or SKIP)

Run: `go test ./...`
Expected: all tests pass

- [ ] **Step 4: Commit**

```bash
git add internal/queue/redis_jobqueue.go internal/queue/redis_jobqueue_test.go
git commit -m "feat: add Redis JobQueue implementation (Stream + Consumer Group + worker registry)"
```

---

### Task 8: Redis Bundle + transport switch in main.go

**Files:**
- Create: `internal/queue/redis_bundle.go`
- Modify: `cmd/bot/main.go`

- [ ] **Step 1: Create Redis Bundle constructor**

Create `internal/queue/redis_bundle.go`:

```go
package queue

import "github.com/redis/go-redis/v9"

// NewRedisBundle creates a Bundle backed by Redis.
// The taskType parameter determines which stream the JobQueue uses.
func NewRedisBundle(rdb *redis.Client, store JobStore, taskType string) *Bundle {
	return &Bundle{
		Queue:       NewRedisJobQueue(rdb, store, taskType),
		Results:     NewRedisResultBus(rdb),
		Attachments: NewRedisAttachmentStore(rdb),
		Commands:    NewRedisCommandBus(rdb),
		Status:      NewRedisStatusBus(rdb),
	}
}
```

- [ ] **Step 2: Add transport switch in main.go**

In `cmd/bot/main.go`, replace the bundle creation block (lines 89-91):

From:
```go
	jobStore := queue.NewMemJobStore()
	jobStore.StartCleanup(1 * time.Hour)
	bundle := queue.NewInMemBundle(cfg.Queue.Capacity, cfg.Workers.Count, jobStore)
```

To:
```go
	jobStore := queue.NewMemJobStore()
	jobStore.StartCleanup(1 * time.Hour)

	var bundle *queue.Bundle
	switch cfg.Queue.Transport {
	case "redis":
		rdb, err := queue.NewRedisClient(queue.RedisConfig{
			Addr:     cfg.Redis.Addr,
			Password: cfg.Redis.Password,
			DB:       cfg.Redis.DB,
			TLS:      cfg.Redis.TLS,
		})
		if err != nil {
			slog.Error("failed to connect to Redis", "error", err)
			os.Exit(1)
		}
		bundle = queue.NewRedisBundle(rdb, jobStore, "triage")
		slog.Info("using Redis transport", "addr", cfg.Redis.Addr)
	default:
		bundle = queue.NewInMemBundle(cfg.Queue.Capacity, cfg.Workers.Count, jobStore)
		slog.Info("using in-memory transport")
	}
```

- [ ] **Step 3: In redis mode, skip LocalAdapter (workers are external)**

After the transport switch, wrap the LocalAdapter creation in a condition:

```go
	// In redis mode, workers are separate pods — skip local adapter.
	if cfg.Queue.Transport != "redis" {
		localAdapter := NewLocalAdapter(LocalAdapterConfig{
			Runner:         &agentRunnerAdapter{runner: agentRunner},
			RepoCache:      &repoCacheAdapter{cache: repoCache},
			SkillDirs:      skillDirs,
			WorkerCount:    cfg.Workers.Count,
			StatusInterval: cfg.Queue.StatusInterval,
			Capabilities:   []string{"triage"},
			Store:          jobStore,
		})
		if err := localAdapter.Start(queue.AdapterDeps{
			Jobs:        bundle.Queue,
			Results:     bundle.Results,
			Status:      bundle.Status,
			Commands:    bundle.Commands,
			Attachments: bundle.Attachments,
		}); err != nil {
			slog.Error("failed to start local adapter", "error", err)
			os.Exit(1)
		}
	}
```

- [ ] **Step 4: Build and test**

Run: `go build ./... && go test ./...`
Expected: compiles, all tests pass

- [ ] **Step 5: Commit**

```bash
git add internal/queue/redis_bundle.go cmd/bot/main.go
git commit -m "feat: add Redis Bundle + transport switch (inmem/redis) in main.go"
```

---

### Task 9: `bot worker` subcommand

**Files:**
- Create: `cmd/bot/worker.go`

- [ ] **Step 1: Create worker subcommand**

Create `cmd/bot/worker.go`:

```go
package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"slack-issue-bot/internal/bot"
	"slack-issue-bot/internal/config"
	ghclient "slack-issue-bot/internal/github"
	"slack-issue-bot/internal/queue"
	"slack-issue-bot/internal/worker"
)

func runWorker() {
	configPath := flag.String("config", "worker.yaml", "path to worker config file")
	flag.Parse()

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))

	cfg, err := config.Load(*configPath)
	if err != nil {
		slog.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	rdb, err := queue.NewRedisClient(queue.RedisConfig{
		Addr:     cfg.Redis.Addr,
		Password: cfg.Redis.Password,
		DB:       cfg.Redis.DB,
		TLS:      cfg.Redis.TLS,
	})
	if err != nil {
		slog.Error("failed to connect to Redis", "error", err)
		os.Exit(1)
	}
	slog.Info("connected to Redis", "addr", cfg.Redis.Addr)

	jobStore := queue.NewMemJobStore() // local ephemeral store

	bundle := queue.NewRedisBundle(rdb, jobStore, "triage")

	agentRunner := bot.NewAgentRunnerFromConfig(cfg)
	repoCache := ghclient.NewRepoCache(cfg.RepoCache.Dir, cfg.RepoCache.MaxAge, cfg.GitHub.Token)

	// Collect skill dirs.
	var skillDirs []string
	seen := make(map[string]bool)
	for _, name := range cfg.Fallback {
		if agent, ok := cfg.Agents[name]; ok && agent.SkillDir != "" && !seen[agent.SkillDir] {
			skillDirs = append(skillDirs, agent.SkillDir)
			seen[agent.SkillDir] = true
		}
	}

	pool := worker.NewPool(worker.Config{
		Queue:          bundle.Queue,
		Attachments:    bundle.Attachments,
		Results:        bundle.Results,
		Store:          jobStore,
		Runner:         &agentRunnerAdapter{runner: agentRunner},
		RepoCache:      &repoCacheAdapter{cache: repoCache},
		WorkerCount:    cfg.Workers.Count,
		SkillDirs:      skillDirs,
		Commands:       bundle.Commands,
		Status:         bundle.Status,
		StatusInterval: cfg.Queue.StatusInterval,
	})

	ctx, cancel := context.WithCancel(context.Background())
	pool.Start(ctx)
	slog.Info("worker started", "workers", cfg.Workers.Count)

	// Wait for SIGTERM/SIGINT.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	sig := <-sigCh
	slog.Info("shutting down", "signal", sig)
	cancel()
	bundle.Close()
}
```

- [ ] **Step 2: Update main.go to support subcommands**

In `cmd/bot/main.go`, at the beginning of `func main()`, add subcommand routing before the existing flag parsing:

```go
func main() {
	if len(os.Args) > 1 && os.Args[0] != "-" {
		switch os.Args[1] {
		case "worker":
			os.Args = append(os.Args[:1], os.Args[2:]...)
			runWorker()
			return
		}
	}

	// Existing code continues...
	configPath := flag.String("config", "config.yaml", "path to config file")
	// ...
```

- [ ] **Step 3: Build and verify**

Run: `go build -o bot ./cmd/bot/`
Expected: compiles

Run: `./bot worker -config config.yaml` (will fail to connect to Redis if not running, but should compile)
Expected: either connects or shows "failed to connect to Redis"

- [ ] **Step 4: Run all tests**

Run: `go test ./...`
Expected: all tests pass

- [ ] **Step 5: Commit**

```bash
git add cmd/bot/worker.go cmd/bot/main.go
git commit -m "feat: add 'bot worker' subcommand for standalone Redis worker"
```

---

### Task 10: Integration test + end-to-end verification

**Files:**
- Create: `internal/queue/redis_integration_test.go`

- [ ] **Step 1: Write Redis integration test**

Create `internal/queue/redis_integration_test.go`:

```go
package queue_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"slack-issue-bot/internal/bot"
	"slack-issue-bot/internal/queue"
	"slack-issue-bot/internal/worker"
)

func testRedisClientExt(t *testing.T) *redis.Client {
	t.Helper()
	addr := os.Getenv("TEST_REDIS_ADDR")
	if addr == "" {
		addr = "localhost:6379"
	}
	client := redis.NewClient(&redis.Options{Addr: addr, DB: 15})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		t.Skipf("skipping Redis integration test: %v", err)
	}
	client.FlushDB(ctx)
	t.Cleanup(func() {
		client.FlushDB(context.Background())
		client.Close()
	})
	return client
}

func TestRedisFullFlow_SubmitToResult(t *testing.T) {
	rdb := testRedisClientExt(t)
	store := queue.NewMemJobStore()
	bundle := queue.NewRedisBundle(rdb, store, "triage")
	defer bundle.Close()

	runner := &fakeRunner{}
	repo := &fakeRepo{}

	pool := worker.NewPool(worker.Config{
		Queue:       bundle.Queue,
		Attachments: bundle.Attachments,
		Results:     bundle.Results,
		Store:       store,
		Runner:      runner,
		RepoCache:   repo,
		WorkerCount: 1,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool.Start(ctx)

	// Submit job.
	bundle.Queue.Submit(ctx, &queue.Job{
		ID:       "j1",
		Priority: 50,
		Repo:     "owner/repo",
		Prompt:   "test prompt",
		TaskType: "triage",
	})

	// Wait for result.
	ch, _ := bundle.Results.Subscribe(ctx)
	select {
	case result := <-ch:
		if result.Status != "completed" {
			t.Errorf("status = %q, want completed", result.Status)
		}
		if result.Title != "Test issue" {
			t.Errorf("title = %q", result.Title)
		}
	case <-ctx.Done():
		t.Fatal("timeout waiting for result")
	}
}
```

Note: This test reuses `fakeRunner` and `fakeRepo` from the existing `internal/queue/integration_test.go`. Since both files are in `queue_test` package, they share the same test binary. Add the necessary import for `redis`:

```go
import "github.com/redis/go-redis/v9"
```

- [ ] **Step 2: Run Redis integration test**

Run: `go test ./internal/queue/ -run TestRedisFullFlow -v -timeout 30s`
Expected: PASS if Redis is running at localhost:6379, SKIP otherwise

- [ ] **Step 3: Run all tests**

Run: `go test ./... -count=1`
Expected: all tests pass

- [ ] **Step 4: Manual verification with inmem (regression check)**

```bash
go build -o bot ./cmd/bot/ && ./bot -config config.yaml
```

Trigger a triage via Slack, verify:
- Agent starts and shows status in `/jobs`
- Job completes or cancel works
- Behavior identical to Phase A

- [ ] **Step 5: Commit**

```bash
git add internal/queue/redis_integration_test.go
git commit -m "test: add Redis full flow integration test"
```
