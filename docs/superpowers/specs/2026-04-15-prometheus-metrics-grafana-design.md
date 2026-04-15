# Prometheus Metrics & Grafana Dashboard

## Summary

Add comprehensive Prometheus metrics to AgentDock via a centralized `internal/metrics/` package, expose `/metrics` endpoint on the existing HTTP server, and provide a Grafana dashboard as ConfigMap for automatic sidecar loading.

## Context

AgentDock currently has `/healthz` (liveness) and `/jobs` (JSON status) endpoints but zero metrics infrastructure. The application has rich operational data (queue depth, agent execution times, LLM token costs, rate limiter rejections, watchdog kills) that goes unrecorded. Adding Prometheus metrics enables alerting, capacity planning, and production visibility.

## Decisions

- **Approach A (centralized)**: All metrics defined in `internal/metrics/metrics.go`, imported by other packages. Chosen over distributed (per-package) or middleware-wrapper approaches for naming consistency and reviewability.
- **Namespace**: All metrics use `agentdock_` prefix with subsystem grouping.
- **Endpoint**: `/metrics` on the existing HTTP server (port 8080), alongside `/healthz` and `/jobs`.
- **Dashboard delivery**: Grafana ConfigMap with `grafana_dashboard: "1"` label for sidecar auto-loading (GitOps).
- **Scrape config**: Both ServiceMonitor CRD and pod annotations provided; user selects based on cluster setup.

## Architecture

```
internal/metrics/metrics.go
  ├── Register()              # called once from main
  ├── Request* counters/histograms
  ├── Queue* gauges/histograms
  ├── Agent* counters/histograms
  ├── Issue* counters
  ├── Handler* counters/gauges
  ├── Watchdog* counters
  ├── External* histograms/counters
  └── Worker* gauges

cmd/agentdock/app.go
  └── http.Handle("/metrics", promhttp.Handler())

Instrumentation points:
  slack/handler.go      → request_total, dedup, rate_limit, concurrent
  bot/workflow.go        → request_duration (end-to-end timer started here)
  bot/result_listener.go → issue_created, issue_rejected, issue_retry, request_duration (observed here)
  worker/pool.go         → worker_active, worker_idle
  worker/executor.go     → agent_execution, prepare_duration, agent cost/tokens
  queue/watchdog.go      → watchdog_kills
  queue/*.go             → queue_depth, queue_submitted, queue_wait, job_duration
  slack/client.go        → external_duration (slack)
  github/issue.go        → external_duration (github)
  queue/redis_*.go       → external_duration (redis)
```

## Metrics Definition (24 custom metrics)

### Request Pipeline (subsystem: `request`)

| Metric | Type | Labels | Source | Description |
|--------|------|--------|--------|-------------|
| `agentdock_request_total` | Counter | `status` (accepted/dedup/rate_limited) | `slack/handler.go` HandleTrigger | Incoming triage requests |
| `agentdock_request_duration_seconds` | Histogram | - | `bot/result_listener.go` handleResult computes `result.FinishedAt - job.SubmittedAt` | End-to-end time from Slack trigger to issue creation |

### Queue (subsystem: `queue`)

| Metric | Type | Labels | Source | Description |
|--------|------|--------|--------|-------------|
| `agentdock_queue_depth` | Gauge | - | `queue/inmem_jobqueue.go` Submit/Ack, `queue/redis_jobqueue.go` Submit/Ack | Current pending job count |
| `agentdock_queue_submitted_total` | Counter | `priority` | `queue/coordinator.go` Submit | Jobs submitted to queue |
| `agentdock_queue_wait_seconds` | Histogram | - | `worker/pool.go` executeWithTracking (now - job.SubmittedAt) | Time from submission to worker pickup |
| `agentdock_queue_job_duration_seconds` | Histogram | `status` (completed/failed) | `worker/pool.go` executeWithTracking (after executeJob returns) | Total job execution time |

### Agent (subsystem: `agent`)

| Metric | Type | Labels | Source | Description |
|--------|------|--------|--------|-------------|
| `agentdock_agent_execution_seconds` | Histogram | `provider` | `worker/executor.go` executeJob (FinishedAt - StartedAt) | Agent process runtime |
| `agentdock_agent_executions_total` | Counter | `provider`, `status` (success/timeout/error/fallback) | `bot/agent.go` Run (per provider attempt) | Agent execution outcomes |
| `agentdock_agent_prepare_seconds` | Histogram | - | `worker/executor.go` executeJob (repo prepare phase) | Time spent cloning/fetching repo |
| `agentdock_agent_tool_calls` | Histogram | `provider` | `worker/pool.go` from StatusReport.ToolCalls at job completion | Tool calls per execution |
| `agentdock_agent_files_read` | Histogram | `provider` | `worker/pool.go` from StatusReport.FilesRead at job completion | Files read per execution |
| `agentdock_agent_cost_usd` | Counter | `provider` | `worker/pool.go` from StatusReport.CostUSD at job completion | Cumulative LLM spend (USD) |
| `agentdock_agent_tokens_total` | Counter | `provider`, `type` (input/output) | `worker/pool.go` from StatusReport at job completion | Cumulative token usage |

### Issue (subsystem: `issue`)

| Metric | Type | Labels | Source | Description |
|--------|------|--------|--------|-------------|
| `agentdock_issue_created_total` | Counter | `confidence` (high/medium/low), `degraded` (true/false) | `bot/result_listener.go` createAndPostIssue | GitHub issues created |
| `agentdock_issue_rejected_total` | Counter | `reason` (low_confidence/no_github) | `bot/result_listener.go` handleResult | Rejected triages |
| `agentdock_issue_retry_total` | Counter | `status` (submitted/exhausted) | `bot/retry_handler.go` | Retry attempts |

### Handler (subsystem: `handler`)

| Metric | Type | Labels | Source | Description |
|--------|------|--------|--------|-------------|
| `agentdock_handler_dedup_rejections_total` | Counter | - | `slack/handler.go` HandleTrigger (isDuplicate=true) | Dedup rejections |
| `agentdock_handler_rate_limit_total` | Counter | `type` (user/channel) | `slack/handler.go` HandleTrigger (allow=false) | Rate limit rejections |
| `agentdock_handler_concurrent_requests` | Gauge | - | `bot/workflow.go` HandleTrigger entry/exit | In-flight triage requests |

### Watchdog (subsystem: `watchdog`)

| Metric | Type | Labels | Source | Description |
|--------|------|--------|--------|-------------|
| `agentdock_watchdog_kills_total` | Counter | `reason` (job_timeout/idle_timeout/prepare_timeout) | `queue/watchdog.go` killAndPublish | Watchdog-initiated kills |

### External Dependencies (subsystem: `external`)

| Metric | Type | Labels | Source | Description |
|--------|------|--------|--------|-------------|
| `agentdock_external_duration_seconds` | Histogram | `service` (slack/github/redis), `operation` | Call sites in `slack/client.go`, `github/issue.go`, `queue/redis_*.go` | External API latency |
| `agentdock_external_errors_total` | Counter | `service`, `operation` | Same call sites, on error | External API errors |

### Worker (subsystem: `worker`)

| Metric | Type | Labels | Source | Description |
|--------|------|--------|--------|-------------|
| `agentdock_worker_active` | Gauge | - | `worker/pool.go` executeWithTracking enter/exit | Busy workers |
| `agentdock_worker_idle` | Gauge | - | Derived: WorkerCount - active | Idle workers |

### Go Runtime (built-in, no custom code)

`promhttp.Handler()` automatically exposes: `go_goroutines`, `go_memstats_*`, `process_cpu_seconds_total`, `process_open_fds`, etc.

## Histogram Buckets

- **Request duration** (end-to-end): `{30, 60, 120, 300, 600, 900, 1200}` seconds (triage takes minutes)
- **Agent execution**: `{30, 60, 120, 300, 600, 900}` seconds
- **Agent prepare** (repo clone): `{1, 5, 10, 30, 60, 120}` seconds
- **Queue wait**: `{1, 5, 10, 30, 60, 120, 300}` seconds
- **External API**: `{0.1, 0.25, 0.5, 1, 2.5, 5, 10}` seconds (standard)

## Grafana Dashboard

### Delivery

- `deploy/grafana/agentdock-dashboard.json` — dashboard JSON
- `deploy/grafana/dashboard-configmap.yaml` — ConfigMap with `grafana_dashboard: "1"` label

### Template Variables

- `$__rate_interval` — Prometheus auto-interval for `rate()`
- `$provider` — multi-select, filters agent provider

### Layout (6 rows, top-down from overview to detail)

**Row 1: Overview**

| Panel | Type | Query |
|-------|------|-------|
| Request Rate | Stat | `sum(rate(agentdock_request_total[$__rate_interval]))` |
| Issue Output Rate | Stat | `sum(rate(agentdock_issue_created_total[$__rate_interval]))` |
| Success Rate | Gauge | `sum(rate(agentdock_issue_created_total[5m])) / (sum(rate(agentdock_issue_created_total[5m])) + sum(rate(agentdock_issue_rejected_total[5m])))` |
| E2E P95 Latency | Stat | `histogram_quantile(0.95, sum(rate(agentdock_request_duration_seconds_bucket[$__rate_interval])) by (le))` |
| Queue Depth | Stat | `agentdock_queue_depth` |
| Active Workers | Stat | `agentdock_worker_active` |

**Row 2: Request Pipeline**

| Panel | Type | Query |
|-------|------|-------|
| Request Distribution | Time series | `sum by (status) (rate(agentdock_request_total[$__rate_interval]))` |
| Rate Limit Hits | Time series | `sum by (type) (rate(agentdock_handler_rate_limit_total[$__rate_interval]))` |
| E2E Latency Heatmap | Heatmap | `sum(rate(agentdock_request_duration_seconds_bucket[$__rate_interval])) by (le)` |

**Row 3: Queue & Workers**

| Panel | Type | Query |
|-------|------|-------|
| Queue Depth Trend | Time series | `agentdock_queue_depth` |
| Queue Wait Time | Time series | P50/P95/P99 of `agentdock_queue_wait_seconds` |
| Job Status | Time series | `sum by (status) (rate(agentdock_queue_job_duration_seconds_count[$__rate_interval]))` |
| Worker Utilization | Time series | `agentdock_worker_active / (agentdock_worker_active + agentdock_worker_idle)` |

**Row 4: Agent Performance**

| Panel | Type | Query |
|-------|------|-------|
| Execution Time by Provider | Time series | P50/P95 of `agentdock_agent_execution_seconds` by `provider` |
| Provider Success Rate | Time series | `sum by (provider) (rate(agentdock_agent_executions_total{status="success"}[$__rate_interval])) / sum by (provider) (rate(agentdock_agent_executions_total[$__rate_interval]))` |
| Fallback Count | Time series | `sum by (provider) (rate(agentdock_agent_executions_total{status="fallback"}[$__rate_interval]))` |
| Avg Tool Calls | Time series | `sum by (provider) (rate(agentdock_agent_tool_calls_sum[$__rate_interval])) / sum by (provider) (rate(agentdock_agent_tool_calls_count[$__rate_interval]))` |
| Cumulative Cost | Stat | `sum by (provider) (agentdock_agent_cost_usd)` |
| Token Usage | Time series | `sum by (provider, type) (rate(agentdock_agent_tokens_total[$__rate_interval]))` |

**Row 5: Issue Output**

| Panel | Type | Query |
|-------|------|-------|
| Issue Trend | Time series | `sum by (confidence) (rate(agentdock_issue_created_total[$__rate_interval]))` |
| Degraded Ratio | Pie chart | `sum by (degraded) (agentdock_issue_created_total)` |
| Rejection Reasons | Bar gauge | `sum by (reason) (agentdock_issue_rejected_total)` |
| Retry Trend | Time series | `sum by (status) (rate(agentdock_issue_retry_total[$__rate_interval]))` |

**Row 6: External Dependencies**

| Panel | Type | Query |
|-------|------|-------|
| Slack API Latency | Time series | P50/P95 of `agentdock_external_duration_seconds{service="slack"}` by `operation` |
| GitHub API Latency | Time series | P50/P95 of `agentdock_external_duration_seconds{service="github"}` by `operation` |
| External Error Rate | Time series | `sum by (service) (rate(agentdock_external_errors_total[$__rate_interval]))` |
| Watchdog Kills | Time series | `sum by (reason) (rate(agentdock_watchdog_kills_total[$__rate_interval]))` |

### Visual Style

- Dark theme
- Row 1 uses large Stat panels for at-a-glance health
- Latency panels: P50 (green) / P95 (yellow) / P99 (red)
- All time series use `$__rate_interval`

## K8s / Scrape Configuration

### ServiceMonitor (`deploy/metrics/servicemonitor.yaml`)

```yaml
apiVersion: monitoring.coreos.com/v1
kind: ServiceMonitor
metadata:
  name: agentdock
  labels:
    app: agentdock
spec:
  selector:
    matchLabels:
      app: agentdock
  endpoints:
    - port: http
      path: /metrics
      interval: 30s
```

### Pod Annotations (added to `deploy/base/deployment.yaml`)

```yaml
annotations:
  prometheus.io/scrape: "true"
  prometheus.io/port: "8080"
  prometheus.io/path: "/metrics"
```

Both provided — use whichever matches the cluster's Prometheus discovery method.

## Files to Create

| File | Purpose |
|------|---------|
| `internal/metrics/metrics.go` | All metric definitions + `Register()` |
| `deploy/metrics/servicemonitor.yaml` | Prometheus Operator CRD |
| `deploy/grafana/agentdock-dashboard.json` | Grafana dashboard JSON |
| `deploy/grafana/dashboard-configmap.yaml` | ConfigMap wrapping dashboard JSON |

## Files to Modify

| File | Change |
|------|--------|
| `go.mod` | Add `github.com/prometheus/client_golang` |
| `cmd/agentdock/app.go` | Import metrics, call `Register()`, add `/metrics` handler |
| `internal/slack/handler.go` | Instrument: request_total, dedup_rejections, rate_limit |
| `internal/bot/workflow.go` | Instrument: request_duration timer start, concurrent_requests gauge |
| `internal/bot/result_listener.go` | Instrument: issue_created, issue_rejected, request_duration observe |
| `internal/bot/retry_handler.go` | Instrument: issue_retry |
| `internal/bot/agent.go` | Instrument: agent_executions_total (per provider attempt) |
| `internal/worker/pool.go` | Instrument: worker_active/idle, queue_wait, job_duration, agent cost/tokens from StatusReport |
| `internal/worker/executor.go` | Instrument: agent_execution_seconds, agent_prepare_seconds |
| `internal/queue/watchdog.go` | Instrument: watchdog_kills |
| `internal/queue/coordinator.go` | Instrument: queue_submitted |
| `internal/queue/inmem_jobqueue.go` | Instrument: queue_depth (Inc on Submit, Dec on Ack) |
| `internal/queue/redis_jobqueue.go` | Instrument: queue_depth + external_duration (redis) |
| `internal/slack/client.go` | Instrument: external_duration (slack operations) |
| `internal/github/issue.go` | Instrument: external_duration (github) |
| `deploy/base/deployment.yaml` | Add prometheus annotations |

## Testing

- Unit test `internal/metrics/metrics.go`: verify all metrics register without panic, verify `Register()` is idempotent.
- Integration: run bot locally, `curl localhost:8080/metrics`, verify all metric names appear with correct types.
- Grafana: import JSON into local Grafana instance, verify panels render (even with zero data).

## Out of Scope

- Alerting rules (Prometheus alertmanager config) — separate concern
- Distributed tracing (OpenTelemetry) — future work
- Custom Prometheus pushgateway for batch jobs — not needed, scrape-based is sufficient
- Metrics cardinality management — label set is bounded by design (no unbounded labels like channel_id or user_id)
