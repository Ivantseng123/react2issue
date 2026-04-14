# AgentDock

[繁體中文](README.md)

AI agent dispatch platform — receives requests from any source, dispatches to CLI agents (claude/codex/opencode) for execution, returns structured results. Currently supports Slack → codebase triage → GitHub Issue workflow.

Single Go binary, supports both in-memory and Redis transports. Workers can run in the same process, separate pods, or on a teammate's machine.

## Quick Start

```bash
cp config.example.yaml config.yaml
# Fill in Slack / GitHub tokens
./run.sh
```

`run.sh` automatically sets up agent skills → build → start.

## Flow

```
@bot or /triage (in thread)
  → dedup + rate limit → read all thread messages
  → repo/branch selection (buttons in thread) → optional description
  → build prompt → Submit to Priority Queue (immediate reply with queue status + cancel button)
  → Worker consumes job from Queue
    → clone repo → mount skills → spawn CLI agent
    → agent explores codebase + evaluates confidence → returns JSON result
  → App receives result → create GitHub issue → post URL to Slack thread
```

## Queue Architecture

Bot uses a producer/consumer queue to decouple Slack event handling from agent execution:

```
┌──────────┐     ┌──────────────┐     ┌─────────────┐     ┌──────────────┐
│ Slack    │────→│ Priority     │────→│ Worker Pool │────→│ Result       │
│ Handler  │     │ Queue        │     │ (N workers) │     │ Listener     │
│          │     │ (channel     │     │             │     │              │
│ dedup +  │     │  priority)   │     │ clone repo  │     │ create issue │
│ rate     │     │              │     │ mount skill │     │ post Slack   │
│ limit    │     │ capacity: 50 │     │ run agent   │     │ cleanup      │
└──────────┘     └──────────────┘     └─────────────┘     └──────────────┘
                       ↑ Submit              ↑ Kill              ↑ Status
                       │                     │                   │
                 ┌─────┴─────────────────────┴───────────────────┘
                 │              CommandBus / StatusBus
                 │        (kill signals, agent status reports)
                 └───────────────────────────────────────────────┘
```

- **Priority Queue**: channel-based priority + FIFO, bounded capacity
- **Worker Pool**: N goroutines consuming jobs, each job has an independent context (cancellable)
- **StatusBus**: workers periodically report agent status (PID, tool calls, files read, cost)
- **CommandBus**: app → worker kill signal channel

### Deployment Modes

| Mode | Transport | Description |
|------|-----------|-------------|
| In-Memory | `queue.transport: inmem` | Everything in one process, Go channel communication (default) |
| Redis Worker | `queue.transport: redis` | App and Worker deployed separately, Redis Stream/Pub/Sub |
| External Worker | Redis + runner binary | Future: external machines run `bot worker`, same Redis |

All three modes use the same interfaces (`JobQueue`, `ResultBus`, `StatusBus`, `CommandBus`, `AttachmentStore`), only the transport layer changes. Switch by changing config only.

#### Redis Mode Architecture

```
┌─────────────┐                    ┌─────────────┐
│  App Pod    │                    │ Worker Pod  │
│             │    Redis Streams   │             │
│ Slack ──→   │──── JobQueue ────→│ consume job │
│ Workflow    │                    │ clone repo  │
│             │←── ResultBus ────│ run agent   │
│ create issue│←── StatusBus ────│ report      │
│ post Slack  │──── CommandBus ──→│ kill signal │
└─────────────┘                    └─────────────┘
```

App doesn't run agents. Workers don't need Slack tokens or GitHub write tokens.

#### External Worker Dependencies

If you download the binary from GitHub Release and run `bot worker` on an external machine, the **binary is not self-contained**. Workers `exec` the following CLIs — install them and ensure they're in `PATH`:

- **At least one agent CLI** (whichever is configured):
  - `@anthropic-ai/claude-code` (npm)
  - `@openai/codex` (npm)
  - `opencode` (see [anomalyco/opencode](https://github.com/anomalyco/opencode) releases)
  - `gemini` (if used)
- **`gh` CLI** (for creating GitHub issues)
- **`git`** (for cloning repos)

If you don't want to manage these dependencies, use the Docker image: `ghcr.io/ivantseng123/agentdock:<version>` includes all runtimes.

**Windows note**: native Windows support for the above CLIs is provided by upstream vendors. If you encounter compatibility issues, use the Docker image (requires WSL2 or Linux VM).

## Triggers

| Method | Example | Description |
|--------|---------|-------------|
| `@bot` mention | `@bot` in thread | Reads all preceding thread messages |
| `/triage` | `/triage` | Interactive repo selection |
| `/triage` + repo | `/triage owner/repo` | Skip repo selection |
| `/triage` + repo + branch | `/triage owner/repo@main` | Start analysis directly |

Bot only operates in **threads**. Triggering directly in a channel prompts "please use in a thread".

## Monitoring & Management

### View Job Status

```bash
curl localhost:8180/jobs | jq .
```

Returns:

```json
{
  "queue_depth": 1,
  "total": 2,
  "jobs": [
    {
      "id": "req-abc123",
      "status": "running",
      "repo": "org/backend",
      "age": "45s",
      "agent": {
        "pid": 12345,
        "command": "claude",
        "alive": true,
        "last_event": "tool_use:Read",
        "last_event_age": "3s",
        "tool_calls": 12,
        "files_read": 8,
        "output_bytes": 15360,
        "cost_usd": 0.042
      }
    },
    {
      "id": "req-def456",
      "status": "pending",
      "repo": "org/frontend",
      "age": "10s",
      "position": 1
    }
  ]
}
```

### Manual Job Termination

```bash
curl -X DELETE localhost:8180/jobs/req-abc123
```

### Slack Cancel

The queue status message posted after submit includes a "Cancel" button.

### Automatic Protection

| Mechanism | Default | Description |
|-----------|---------|-------------|
| Job timeout | 20m | Maximum job lifecycle |
| Agent idle timeout | 5m | stream-json agent auto-terminates after no events |
| Prepare timeout | 3m | Clone/setup timeout auto-terminates |

After timeout, bot notifies the Slack user and clears dedup so users can re-trigger.

## Configuration

See `config.example.yaml` for all options.

```yaml
auto_bind: true

channel_defaults:
  branch_select: true
  default_labels: ["from-slack"]

# Agent config
agents:
  claude:
    command: claude
    args: ["--print", "--output-format", "stream-json", "-p", "{prompt}"]
    timeout: 15m
    skill_dir: ".claude/skills"
    stream: true                      # enable real-time event tracking
  opencode:
    command: opencode
    args: ["--prompt", "{prompt}"]
    timeout: 5m
    skill_dir: ".opencode/skills"

active_agent: claude
providers: [claude, opencode]

# Queue config
queue:
  capacity: 50                        # queue limit
  transport: inmem                    # inmem | redis
  job_timeout: 20m                    # watchdog: max job lifecycle
  agent_idle_timeout: 5m              # stream-json: no-event timeout
  prepare_timeout: 3m                 # clone/setup timeout
  status_interval: 5s                 # worker status report frequency

workers:
  count: 3                            # worker pool size

# Redis config (used when transport: redis)
# redis:
#   addr: redis:6379
#   password: ""
#   db: 0

channel_priority:
  # C_INCIDENTS: 100                  # production incidents get priority
  default: 50

prompt:
  language: "English"
  extra_rules:
    - "List all related file names with full paths"
```

### Agent Stream Mode

Claude supports `--output-format stream-json`, enabling real-time tracking:
- Current tool in use (Read, Bash, Grep...)
- Files read count
- Text generated
- Cost (cost_usd, input/output tokens)

Agents without stream support (opencode, codex) only track PID + alive status.

### Agent Skills

Skills are sent with each Job (`Job.Skills` field). Workers write skill files (supporting full directory trees: SKILL.md + examples + references) into the cloned repo, and agent CLIs auto-discover them on startup. No manual installation needed.

```
agents/
  skills/
    triage-issue/
      SKILL.md           # triage skill — agent analyzes codebase, returns structured result
  setup.sh               # local dev: create symlinks (run.sh calls this automatically)
```

#### Dynamic Skill Loading (Remote)

In addition to baked-in skills, you can dynamically load skills from npm registry via a separate `skills.yaml`:

```yaml
# skills.yaml (mounted via k8s ConfigMap)
skills:
  triage-issue:
    type: local
    path: agents/skills/triage-issue

  code-review:
    type: remote
    package: "@team/skill-code-review"
    version: "latest"

cache:
  ttl: 5m    # Remote skill cache TTL
```

In `config.yaml`, specify the path:
```yaml
skills_config: "/etc/agentdock/skills.yaml"
```

**Features:**
- **TTL cache + singleflight**: avoid duplicate fetches, re-fetch only when cache expires
- **Two-layer fallback**: remote fails → use cached old version → use baked-in → skip
- **Negative cache**: failed skills don't retry within TTL
- **Startup warmup**: prefetch all remote skills on app startup
- **Hot reload**: fsnotify watches skills.yaml, auto-reloads on ConfigMap update
- **Same-name conflict fail fast**: local and remote skills with same name → immediate error
- **File validation**: single skill < 1MB, job total < 5MB, extension whitelist, path traversal protection

**NPM package convention:**
```
node_modules/@team/skill-code-review/
  skills/
    code-review/
      SKILL.md           # required
      examples/           # optional
      references/         # optional
```

Private registries require separate `.npmrc` configuration (mount via k8s Secret to `/home/node/.npmrc`).

## Agent Behavior

After receiving the prompt, the agent:
1. Loads triage-issue skill
2. Explores codebase (using its own built-in tools)
3. Evaluates confidence (low → reject)
4. Outputs structured JSON result (does not create issue directly):

```json
{
  "status": "CREATED",
  "title": "Login page broken after 3 failed attempts",
  "body": "## Problem\n...",
  "labels": ["bug"],
  "confidence": "high",
  "files_found": 5,
  "open_questions": 0
}
```

App receives result:
- `confidence=low` → don't create issue, notify user
- `files=0` or `questions>=5` → create issue without triage section
- Normal → create full issue + post to Slack thread

## Slack App Setup

Bot Token Scopes:
- `chat:write`, `channels:read`, `channels:history`, `users:read`, `commands`
- Private channels: `groups:history`, `groups:read`

Event Subscriptions:
- `app_mention`
- auto-bind: `member_joined_channel`, `member_left_channel`

Interactivity:
- Enabled (for repo/branch selection buttons + cancel button + description modal)

Slash Command:
- `/triage`

Socket Mode enabled, App-Level Token scope `connections:write`.

## Deployment

### Local (In-Memory Mode)

```bash
./run.sh
# or
go build -o bot ./cmd/bot/ && ./bot -config config.yaml
```

### Local (Redis Mode)

```bash
# Start Redis
redis-server --daemonize yes

# App (handles Slack events, creates issues)
./bot -config config.yaml   # config: queue.transport: redis

# Worker (consumes jobs, runs agents) — can run multiple
./bot worker -config worker.yaml
```

### External Worker (Teammate's Machine)

Teammates don't need any config files, just binary + env vars:

```bash
# Prerequisites: agent CLI installed and logged in (e.g. claude login)
REDIS_ADDR=redis.company.com:6379 GITHUB_TOKEN=ghp_xxx ./bot worker
```

Custom agent:
```bash
REDIS_ADDR=redis.company.com:6379 GITHUB_TOKEN=ghp_xxx PROVIDERS=codex ./bot worker
```

Workers have built-in default configs for three agents (claude/codex/opencode), no YAML needed. Redis address and tokens via env vars.

### Docker / Kubernetes

Image includes three agent CLIs: claude, codex, opencode.

> **Note: Docker containers can only use API key authentication, not OAuth login.** Agent CLI OAuth (e.g. `claude login`) is bound to the local keychain and cannot be transferred to containers. For personal machines using OAuth, use the "External Worker" method above (native binary).

```bash
docker build -t agentdock .

# App mode (inmem, single machine)
docker run -e SLACK_BOT_TOKEN=xoxb-... \
           -e SLACK_APP_TOKEN=xapp-... \
           -e GITHUB_TOKEN=ghp_... \
           -e ANTHROPIC_API_KEY=sk-ant-... \
           agentdock

# Worker mode (Redis, standalone job consumer)
docker run -e REDIS_ADDR=redis:6379 \
           -e GITHUB_TOKEN=ghp_... \
           -e PROVIDERS=claude \
           -e ANTHROPIC_API_KEY=sk-ant-... \
           agentdock worker
```

#### Agent Authentication Methods

| Execution | Auth Method | Use Case |
|-----------|-------------|----------|
| Native binary (`./bot worker`) | OAuth login (`claude login` etc.) | Personal machine, own Pro/Max quota |
| Docker / k8s | API key (env var) | Automated deployment, company API quota |

#### Agent Selection & API Key

Workers select agents via `PROVIDERS` env var (comma-separated, tried in order), no config file changes needed:

```bash
# Use claude
docker run -e PROVIDERS=claude -e ANTHROPIC_API_KEY=sk-ant-... ...

# Use codex, fallback to claude (tried in order)
docker run -e PROVIDERS=codex,claude -e OPENAI_API_KEY=sk-... -e ANTHROPIC_API_KEY=sk-ant-... ...

# Use opencode
docker run -e PROVIDERS=opencode -e ANTHROPIC_API_KEY=sk-ant-... ...
```

| Agent | API Key Env Var | How to Get |
|-------|----------------|------------|
| claude | `ANTHROPIC_API_KEY` | [console.anthropic.com](https://console.anthropic.com) |
| codex | `OPENAI_API_KEY` | [platform.openai.com](https://platform.openai.com) |
| opencode | `ANTHROPIC_API_KEY` | [console.anthropic.com](https://console.anthropic.com) |

Only the API keys for agents listed in `PROVIDERS` are needed.

#### All Environment Variables

| Env Var | Purpose | Required |
|---------|---------|----------|
| `SLACK_BOT_TOKEN` | Slack Bot token | App mode |
| `SLACK_APP_TOKEN` | Slack App-Level token | App mode |
| `GITHUB_TOKEN` | GitHub token (App: read+write, Worker: read) | Yes |
| `REDIS_ADDR` | Redis connection address | Redis mode |
| `REDIS_PASSWORD` | Redis password | If password set |
| `PROVIDERS` | Agent provider order (comma-separated) | No (defaults to config) |
| `ACTIVE_AGENT` | Primary agent | No (defaults to config) |
| `CLAUDE_AUTH_TOKEN` | Claude CLI auth | When using claude |
| `OPENAI_API_KEY` | Codex CLI auth | When using codex |
| `ANTHROPIC_API_KEY` | OpenCode CLI auth | When using opencode |

### Kubernetes

Using Kustomize:

```
deploy/
  base/                          # common deployment (in repo)
    kustomization.yaml
    deployment.yaml
  overlays/
    example/                     # template (in repo)
      kustomization.yaml.example
      secret.yaml.example
    <your-env>/                  # actual config (gitignored)
      kustomization.yaml
      secret.yaml
```

```bash
cp deploy/overlays/example/*.example deploy/overlays/my-env/
vi deploy/overlays/my-env/kustomization.yaml
vi deploy/overlays/my-env/secret.yaml
kubectl apply -k deploy/overlays/my-env/
```

### CI/CD

Automated via [release-please](https://github.com/googleapis/release-please):

1. Write Conventional Commits (`feat:`, `fix:`, `chore:`, etc.)
2. release-please auto-maintains a Release PR (version bump + CHANGELOG)
3. Merge Release PR → auto-creates GitHub Release + tag
4. GHA builds Docker image → push to `ghcr.io`

## Architecture

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

## Testing

```bash
go test ./...   # 150 tests (Redis tests auto-skip if no Redis)
```

## HTTP Endpoints

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/healthz` | GET | Health check |
| `/jobs` | GET | List all job states (with agent tracking) |
| `/jobs/{id}` | DELETE | Terminate a specific job |

## License

MIT
