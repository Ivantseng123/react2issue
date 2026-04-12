# Redis Transport + Adapter Architecture

## Problem

react2issue 目前所有元件跑在同一個 process，用 in-memory channel 通訊。這限制了：
- Worker 無法獨立 scale（agent 執行 15 分鐘吃 CPU/memory，與輕量 Slack app 搶資源）
- 無法在不同機器跑 worker
- 單點故障 — process crash 全部停擺

## Scope

**做：**
- Redis transport 實作（五個現有 interface）
- App / Worker 拆分為獨立 process（同 binary 雙 entrypoint）
- Adapter 抽象 + Capabilities 路由
- Worker 註冊與監控
- Config 切換 inmem / redis

**不做：**
- 外部 worker（公網個人電腦）— 預留擴展空間但不實作
- GitHub Actions adapter — 未來擴展
- 統一 EventBus — 保留分離 interfaces
- Web UI dashboard

**預留擴展：**
- 外部 lightweight runner binary 用同一個 Redis transport，零代碼改動
- 新 adapter type 只需實作 `Adapter` interface + config

## Architecture

```
┌─────────────────────────────────────────────────────┐
│                    App Pod                           │
│                                                     │
│  Slack Socket Mode → Handler → Workflow             │
│                                    │                │
│                              Coordinator            │
│                          (route by capability)      │
│                                    │                │
│                              Redis Streams          │
│                                    │                │
│  ResultListener ← Redis(results)   │                │
│  StatusListener ← Redis(status)    │                │
│  Watchdog (stuck job detection)    │                │
│  HTTP /jobs, /workers              │                │
└────────────────────────────────────┼────────────────┘
                                     │
                              ┌──────┴──────┐
                              │    Redis    │
                              └──────┬──────┘
                                     │
              ┌──────────────────────┼──────────────────────┐
              │                      │                      │
   ┌──────────┴──────────┐ ┌────────┴────────┐  ┌──────────┴──────────┐
   │  Worker Pod (local) │ │ Worker Pod (local)│  │  Future: External  │
   │  LocalAdapter       │ │ LocalAdapter      │  │  Runner Binary     │
   │  claude agent       │ │ claude agent      │  │  (same protocol)   │
   └─────────────────────┘ └──────────────────┘  └─────────────────────┘
```

## Adapter Abstraction

### Interface

```go
type Adapter interface {
    Name() string
    Capabilities() []string
    Start(deps AdapterDeps) error
    Stop() error
}

type AdapterDeps struct {
    Jobs        JobQueue
    Results     ResultBus
    Status      StatusBus
    Commands    CommandBus
    Attachments AttachmentStore
}
```

### LocalAdapter

Wraps the existing `worker.Pool`. Current behavior is unchanged — this is a refactor, not a rewrite.

```go
type LocalAdapter struct {
    pool *worker.Pool
    caps []string
}
```

### Coordinator

Routes jobs to the correct adapter based on `TaskType` and adapter capabilities. Lives in the app process.

```go
type Coordinator struct {
    adapters []Adapter
    bundles  map[string]*Bundle // task_type → bundle (for per-type streams in redis mode)
}

// SubmitJob finds the right adapter and submits to its queue.
func (c *Coordinator) SubmitJob(ctx context.Context, job *Job) error {
    // Find bundle for this task type, submit to its JobQueue
}
```

**Wiring in main.go (redis mode):**
```go
coordinator := queue.NewCoordinator()
for _, adapterCfg := range cfg.Adapters {
    // Workers are separate pods — coordinator only needs the submission side
    for _, cap := range adapterCfg.Capabilities {
        coordinator.RegisterStream(cap, redisBundle)
    }
}
// Workflow calls coordinator.SubmitJob() instead of bundle.Queue.Submit()
```

**Wiring in main.go (inmem mode):**
```go
coordinator := queue.NewCoordinator()
localAdapter := NewLocalAdapter(inmemBundle, workerPool, cfg)
coordinator.RegisterAdapter(localAdapter)
localAdapter.Start(AdapterDeps{...})
// Same interface — workflow calls coordinator.SubmitJob()
```

- **inmem mode:** Coordinator calls the matching adapter's JobQueue directly.
- **redis mode:** Each task type maps to a dedicated stream (`r2i:jobs:{task_type}`). Adapters subscribe only to streams matching their capabilities. Streams and consumer groups are created on app startup via `XGROUP CREATE ... MKSTREAM`. If the group already exists, the error is ignored (idempotent).
- **Claim mechanism:** Redis consumer groups guarantee no duplicate consumption. No custom first-write-wins needed.
- **No-subscriber handling:** If a job is submitted to a task type with no subscribed workers, it remains in the stream. Watchdog detects it via prepare timeout (3 min) and notifies the Slack thread.

### Job Changes

```go
type Job struct {
    // ...existing fields...
    TaskType string `json:"task_type"` // "triage", "code-review", etc.
}
```

Default `TaskType` is `"triage"` for backwards compatibility.

## Redis Primitive Mapping

| Interface | Redis Primitive | Key Pattern | Semantics |
|-----------|----------------|-------------|-----------|
| JobQueue | Stream + Consumer Group | `jobs:{task_type}` | One job to one worker, ack required |
| ResultBus | Stream + Consumer Group | `jobs:results` | Persistent, app consumes reliably |
| StatusBus | Pub/Sub | `jobs:status` | Broadcast, loss tolerable |
| CommandBus | Pub/Sub | `jobs:commands` | Broadcast, worker filters by job_id |
| AttachmentStore | Hash | `jobs:attachments:{job_id}` | Metadata + download URL |

### JobQueue Details

**Submit (App):**
```
XADD jobs:{task_type} * job_id {id} payload {json}
```

**Receive (Worker):**
```
XREADGROUP GROUP workers consumer-{pod_name} COUNT 1 BLOCK 5000 STREAMS jobs:{task_type} >
```

**Ack (Worker):**
```
XACK jobs:{task_type} workers {message_id}
```

**Crash recovery:** Each worker runs an `XAUTOCLAIM` loop on startup and periodically (every 60 seconds). It reclaims messages that have been pending for longer than 2x the status interval (i.e., 10 seconds idle = likely dead worker, not a slow job). To distinguish slow jobs from dead workers, `XAUTOCLAIM` is only attempted for messages whose consumer has no active heartbeat in `workers:{worker_id}` (TTL expired). This prevents reclaiming jobs from workers that are alive but running long agents.

### ResultBus Details

**Publish (Worker):**
```
XADD jobs:results * job_id {id} payload {json}
```

**Subscribe (App):**
```
XREADGROUP GROUP app app-0 COUNT 1 BLOCK 5000 STREAMS jobs:results >
```

### StatusBus Details

**Report (Worker):**
```
PUBLISH jobs:status {json}
```

**Subscribe (App):**
```
SUBSCRIBE jobs:status
```

Status reports arrive every 5 seconds. Missing one or two during reconnect is acceptable.

### CommandBus Details

**Send (App):**
```
PUBLISH jobs:commands {json with job_id + action}
```

**Receive (Worker):**
```
SUBSCRIBE jobs:commands
```

Worker checks if `job_id` matches a job it's currently running.

### AttachmentStore Details

**Prepare (App):**
```
HSET jobs:attachments:{job_id} meta {json}
```

**Resolve (Worker):**
```
HGET jobs:attachments:{job_id} meta
```

Worker downloads files from URLs in the metadata (internal network accessible).

## Deployment Model

### Dual Entrypoint, Single Binary

```bash
bot serve -config config.yaml    # App: Slack + HTTP + listeners
bot worker -config worker.yaml   # Worker: consume queue + run agents
```

### App Pod Responsibilities

- Slack Socket Mode event handling
- Workflow orchestration (thread context, repo/branch selection)
- Job submission to Redis
- ResultListener: consume results, create GitHub issues, post to Slack
- StatusListener: consume status, update JobStore, serve /jobs API
- Watchdog: detect stuck jobs, send kill commands
- HTTP endpoints: /healthz, /jobs, /workers

Does NOT run agents. Does NOT clone repos.

### Worker Pod Responsibilities

- Consume jobs from Redis
- Clone/fetch repos
- Mount skills
- Spawn agent CLI (claude/opencode)
- Stream-parse agent output, report status to Redis
- Report results to Redis
- Receive kill commands, SIGTERM agent

Does NOT need Slack token. Does NOT need GitHub write token. Only needs:
- Redis connection
- GitHub read token (for clone)
- Agent CLI on PATH

### Worker Registration

Workers register via Redis hash with TTL heartbeat:

```
HSET workers:{worker_id} status alive started_at {ts} agents "claude" capabilities "triage"
EXPIRE workers:{worker_id} 30
```

Heartbeat every 10 seconds refreshes the TTL. App lists workers via `SCAN r2i:workers:*`.

The existing `JobQueue` interface has `Register`, `Unregister`, and `ListWorkers` methods. For Redis mode, these delegate to the Redis hash-based registration above. For inmem mode, they continue using the current in-memory map.

All Redis keys are prefixed with `r2i:` (react2issue) to avoid namespace collisions.

### /workers Endpoint

```json
{
  "workers": [
    {
      "worker_id": "worker-pod-abc",
      "status": "alive",
      "agents": ["claude"],
      "capabilities": ["triage"],
      "current_job": "20260410-172825-4323",
      "uptime": "2h30m"
    }
  ]
}
```

### Watchdog Enhancement

Existing behavior: detect stuck jobs by timeout, send kill via CommandBus.

New: if a job's worker disappears from the registry (heartbeat expired), mark job as failed and clear dedup so user can re-trigger.

## Transport Switch

```yaml
# Local development / testing — unchanged
queue:
  transport: inmem
workers:
  count: 3
agents:
  claude: ...
```

```yaml
# K8s deployment — app dispatches, workers execute
queue:
  transport: redis
redis:
  addr: redis:6379
  password: ""
  db: 0
```

### Common Bundle Type

Both transport implementations return a common `Bundle` struct with interface-typed fields:

```go
type Bundle struct {
    Queue       JobQueue
    Results     ResultBus
    Status      StatusBus
    Commands    CommandBus
    Attachments AttachmentStore
}
```

`NewInMemBundle` and `NewRedisBundle` both return `*Bundle`. The existing `InMemBundle` is refactored to use this common type (fields change from concrete types to interfaces). Downstream code already accesses fields through the interface — no consumer changes needed.

### Code Structure

```go
// cmd/bot/main.go
var bundle *queue.Bundle
switch cfg.Queue.Transport {
case "redis":
    bundle = queue.NewRedisBundle(cfg.Redis, jobStore)
    // No local worker pool — workers are separate pods
case "inmem":
    bundle = queue.NewInMemBundle(cfg.Queue.Capacity, cfg.Workers.Count, jobStore)
    // Start local worker pool (existing behavior)
}
```

All downstream code (ResultListener, StatusListener, Watchdog, HTTP handlers) works unchanged.

## Config

### App (config.yaml for redis mode)

```yaml
queue:
  transport: redis

redis:
  addr: redis:6379
  password: ""
  db: 0

adapters:
  local:
    type: local
    capabilities: [triage]
    workers: 3
    agents:
      claude:
        command: claude
        args: ["--print", "--output-format", "stream-json", "-p", "{prompt}"]
        timeout: 15m
        skill_dir: ".claude/skills"
        stream: true
```

### Worker (worker.yaml)

```yaml
redis:
  addr: redis:6379
  password: ""
  db: 0

capabilities: [triage]

agents:
  claude:
    command: claude
    args: ["--print", "--output-format", "stream-json", "-p", "{prompt}"]
    timeout: 15m
    skill_dir: ".claude/skills"
    stream: true

github:
  token: ${GITHUB_TOKEN}

repo_cache:
  dir: /data/repos
  max_age: 1h
```

## RedisConfig

```go
type RedisConfig struct {
    Addr     string `yaml:"addr"`
    Password string `yaml:"password"`
    DB       int    `yaml:"db"`
    TLS      bool   `yaml:"tls"`
}
```

Environment override: `REDIS_ADDR`, `REDIS_PASSWORD`.

## Attachment Serving

For Redis mode, the app stores downloaded Slack attachments to a local temp directory and serves them via an HTTP endpoint:

```
GET /attachments/{job_id}/{filename}
```

`AttachmentStore.Prepare` writes the file to disk and stores the URL (`http://app-service:8180/attachments/...`) in the Redis hash. Workers download via this URL. The endpoint is internal-only (k8s ClusterIP service).

## Agent Liveness in /jobs

The existing `isProcessAlive(pid)` uses OS signal 0, which only works when app and worker share a host. For Redis mode, liveness is determined by `StatusReport.Alive` field (sent by the worker) combined with worker heartbeat status. The PID check is only used when `transport: inmem`.

## Graceful Shutdown

When a worker pod receives SIGTERM (e.g., k8s rolling update):

1. Stop accepting new jobs (stop `XREADGROUP` loop)
2. If an agent is running, send SIGTERM to the agent process
3. Wait up to `WaitDelay` (10s) for agent to exit, then SIGKILL
4. The unfinished job's result is published as `failed` with error "worker shutting down"
5. The job can be retried by the user (dedup is cleared via the failure path)

Kubernetes `terminationGracePeriodSeconds` should be set to at least 20s (10s agent wait + buffer).

For non-interruptible long jobs, a future enhancement could drain the worker (finish current job before shutdown) controlled by a config flag.

## Reconnection & Error Handling

- `go-redis` has built-in auto-reconnect
- Worker's `XREADGROUP` loop resumes from last position after reconnect (consumer group tracks offset)
- Pub/Sub messages lost during disconnect are acceptable: status re-sends every 5s, kill commands can be retried
- Unacked jobs in consumer group pending list are reclaimed by other workers after visibility timeout

## Package Structure

### New Files

```
internal/queue/
  redis_jobqueue.go
  redis_resultbus.go
  redis_statusbus.go
  redis_commandbus.go
  redis_attachments.go
  redis_bundle.go
  adapter.go              # Adapter interface + AdapterDeps
  coordinator.go          # Capability routing

internal/config/
  config.go               # RedisConfig, AdapterConfig additions

cmd/bot/
  worker.go               # bot worker subcommand
```

### Modified Files

```
cmd/bot/main.go           # Transport switch, coordinator wiring
internal/queue/job.go     # TaskType field
internal/queue/httpstatus.go  # /workers endpoint
```

### Unchanged Files

```
internal/bot/*            # workflow, agent, prompt, parser, listeners
internal/worker/*         # pool, executor, status
internal/slack/*
internal/github/*
```

## Future Extension Path

### External Worker (Personal Computer)

Same `bot worker -config worker.yaml` binary, pointing `redis.addr` to a public Redis endpoint (with TLS + auth). Zero code changes. Requires Redis to be externally accessible (via VPN or public ingress with authentication).

### New Adapter Types

Implement `Adapter` interface + add config section. Examples:
- GitHub Actions adapter: `workflow_dispatch` trigger, webhook result collection
- gRPC adapter: remote worker connects via gRPC streaming
- HTTP adapter: long-poll based job consumption

### New Task Types

Add capability string to adapters, set `TaskType` on jobs. Coordinator routes automatically. New Redis stream created per task type.

## JobStore

### App Side

Remains in-memory (`MemJobStore`) on the app side. It is a view cache, not the source of truth. Workers report state via StatusBus/ResultBus, app updates JobStore accordingly. No need to put JobStore in Redis.

### Worker Side

The existing `worker.Pool` depends on `JobStore` for two things:

1. **Pre-execution check** (`pool.go:88`): `store.Get(job.ID)` to check if cancelled while pending
2. **Status updates** (`executor.go:63`, `pool.go:174`): `store.UpdateStatus(job.ID, ...)`

For Redis mode, workers get a **local ephemeral `MemJobStore`** that tracks only in-flight jobs:
- Worker creates a `MemJobStore` entry when it receives a job from the stream
- Status updates go to both the local store AND `StatusBus` (which the app consumes)
- Entry is deleted after the job completes

This requires no changes to `worker.Pool` — it receives a `JobStore` via config, which happens to be a local instance instead of the app's shared instance. The `worker.Pool` code remains unchanged.

### QueuePosition and QueueDepth

For Redis mode, `QueuePosition` returns 0 (not meaningful with consumer groups — jobs are distributed, not queued in a visible line). `QueueDepth` uses `XLEN` on the stream, which returns total unprocessed entries. These are used for Slack queue position messages and the `/jobs` endpoint — returning approximate values is acceptable.

## Mapping to Original Issue

| Issue Concept | This Design | Notes |
|---------------|-------------|-------|
| EventBus | Separated interfaces (JobQueue, ResultBus, StatusBus, CommandBus) | Typed, semantic-specific |
| Adapter | `Adapter` interface with `AdapterDeps` | Composable with existing interfaces |
| Coordinator | `Coordinator` with capability routing | Routes by TaskType |
| Local Adapter (exec) | `LocalAdapter` wrapping `worker.Pool` | Existing behavior unchanged |
| Remote Adapter (gRPC) | Future extension | Interface ready |
| GitHub Actions Adapter | Future extension | Interface ready |
| Claim mechanism | Redis consumer groups | Automatic, no custom CAS |
| worker.online/health | Redis hash + TTL heartbeat | /workers endpoint |
| task.progress | StatusBus (Pub/Sub) | Every 5s with tool_calls, cost, etc. |
| Phase 1 (event bus + local) | Already done (inmem bundle) | ✅ |
| Phase 2 (worker registry) | This design | ✅ |
| Phase 3 (remote adapter) | Future, interface ready | Deferred |
| Phase 4 (GitHub Actions) | Future, interface ready | Deferred |
