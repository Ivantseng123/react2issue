# Internal Architecture

[繁體中文](internals.md)

## Directory Layout

```
cmd/bot/
  main.go                    # entry point, transport switch, Socket Mode event loop
  local_adapter.go           # LocalAdapter: wraps worker.Pool for inmem mode
  worker.go                  # `bot worker` subcommand for standalone Redis worker
internal/
  config/config.go           # YAML config: agents, queue, redis, channels, prompt
  bot/
    workflow.go              # trigger → interact → build prompt → queue.Submit
    agent.go                 # AgentRunner: spawn CLI agent with RunOptions + stream
    parser.go                # parse ===TRIAGE_RESULT=== JSON (+ legacy fallback)
    prompt.go                # build user prompt for CLI agent
    result_listener.go       # ResultBus → create issue / retry button → post Slack
    retry_handler.go         # Retry button interaction → re-submit job to queue
    status_listener.go       # StatusBus → update JobStore agent tracking
    enrich.go                # expand Mantis URLs in messages
  slack/
    client.go                # PostMessage/PostSelector/PostMessageWithButton/...
    handler.go               # TriggerEvent dedup, rate limiting
  github/
    issue.go                 # CreateIssue via GitHub API
    repo.go                  # RepoCache: clone, fetch, branch list, checkout
    discovery.go             # GitHub API repo discovery with cache
  queue/
    interface.go             # JobQueue, ResultBus, CommandBus, StatusBus, JobStore
    adapter.go               # Adapter interface + AdapterDeps
    coordinator.go           # Coordinator: JobQueue decorator, routes by TaskType
    bundle.go                # Common Bundle struct (transport-agnostic)
    job.go                   # Job, JobResult, JobState, AttachmentMeta
    inmem_*.go               # In-memory transport implementations
    redis_*.go               # Redis transport implementations (Stream, Pub/Sub, Hash)
    redis_bundle.go          # NewRedisBundle factory
    redis_client.go          # Redis client construction helper
    memstore.go              # MemJobStore (in-memory job state)
    priority.go              # container/heap priority queue
    registry.go              # ProcessRegistry (cancel-based kill)
    stream.go                # StreamEvent, ReadStreamJSON, ReadRawOutput
    watchdog.go              # Stuck job detection (timeout + idle + prepare)
    httpstatus.go            # GET /jobs, DELETE /jobs/{id}
  worker/
    pool.go                  # Worker pool with command listener + status reporting
    executor.go              # Single job execution (clone, skill, agent, parse)
    status.go                # statusAccumulator (stream event aggregation)
  skill/
    config.go                # skills.yaml parsing (SkillsFileConfig, SkillConfig)
    validate.go              # File validation (size, whitelist, path safety)
    npx.go                   # NPM package install + skill directory scanning
    loader.go                # SkillLoader: cache, singleflight, fallback, warmup
    watcher.go               # fsnotify hot reload for skills.yaml
  mantis/                    # Mantis bug tracker URL enrichment
agents/
  skills/
    triage-issue/SKILL.md    # Agent skill: triage → structured JSON result
  setup.sh                   # Setup symlinks for local dev
deploy/
  base/                      # Kustomize base (deployment)
  overlays/example/          # Overlay template (secret.yaml.example)
```
