# Configuration

[繁體中文](configuration.md)

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

## Agent Stream Mode

Claude supports `--output-format stream-json`, enabling real-time tracking:
- Current tool in use (Read, Bash, Grep...)
- Files read count
- Text generated
- Cost (cost_usd, input/output tokens)

Agents without stream support (opencode, codex) only track PID + alive status.

## Agent Skills

Skills are sent with each Job (`Job.Skills` field). Workers write skill files (supporting full directory trees: SKILL.md + examples + references) into the cloned repo, and agent CLIs auto-discover them on startup. No manual installation needed.

```
agents/
  skills/
    triage-issue/
      SKILL.md           # triage skill — agent analyzes codebase, returns structured result
  setup.sh               # local dev: create symlinks (run.sh calls this automatically)
```

### Dynamic Skill Loading (Remote)

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
