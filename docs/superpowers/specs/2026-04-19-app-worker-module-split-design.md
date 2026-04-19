# App / Worker Module Split — Design

**Date:** 2026-04-19
**Status:** Proposed (awaiting review)
**Related:** Issue #63 as originating context (actual `extra_args` implementation deferred to follow-up). Parent umbrella #54.

## Problem

`agentdock` is currently a single Go module with a single `Config` struct shared by app and worker. Every design discussion wastes time re-litigating the boundary between app (Slack orchestrator) and worker (agent-CLI executor), because the struct, the load flow, and several internal packages are shared.

The user has decided that app and worker are two fully independent entities (see memory: `app-worker-distinction`) and that the codebase should reflect that via module-level separation, with the long-term option of splitting into separate repositories (see memory: `app-worker-future-split`).

Originating use case: worker-side agent CLI behavior control (issue #63). That feature was blocked on deciding whether config should carry extra fields, which in turn exposed that the app/worker boundary in the code is not expressed strongly enough to reason about without ambiguity. Hence this refactor precedes #63.

## Goals

1. App, worker, and their shared types become **three separate Go modules** under one git repo, with dependencies only flowing `app → shared` and `worker → shared`. Module boundaries prevent accidental coupling by making cross-cuts require explicit import paths.
2. Config schema splits into `AppConfig` (owned by `app` module) and `WorkerConfig` (owned by `worker` module). **No shared Config struct**. Users maintain two yaml files: `app.yaml` and `worker.yaml`.
3. A new `agentdock migrate` command converts legacy single-file `config.yaml` into the two new files, with a `.v1.bak` backup.
4. **All existing user-observable behavior is preserved**, including inmem mode. The only user-visible change is the config file split.
5. **CI/CD pipeline is kept untouched** — single binary `agentdock`, single Docker image `ghcr.io/ivantseng123/agentdock:X`, single homebrew formula, single release-please flow. Binary dispatches to `app.Run()` or `worker.Run()` via cobra subcommands.
6. Module boundaries are enforced by an `import_direction_test.go` in the `shared` module.

## Non-goals

- Splitting the git repository (future step).
- Splitting the binary into `agentdock-app` and `agentdock-worker` (single binary retained).
- Per-module Docker images (`Dockerfile.release` unchanged).
- Per-module release versioning / release-please manifest mode (single tag retained).
- Issue #63 `extra_args` implementation (deferred to follow-up, 1-2 days once refactor lands).
- Fixing the `mergeBuiltinAgents` all-or-nothing merge bug (the parent #63 concern; deferred).
- Agent CLI environment isolation (XDG / HOME redirect to defeat `oh my opencode` style plugin pollution) — deferred as low-frequency concern.

## Design

### Module Structure

```
agentdock/                            # git repo root
  go.mod                              # module github.com/Ivantseng123/agentdock
                                      # hosts cmd/agentdock (single binary entry)
  go.work                             # dev convenience; release build uses replace directive

  cmd/agentdock/
    main.go, root.go
    app.go                            # cobra 'app' subcommand → calls app.Run()
    worker.go                         # cobra 'worker' subcommand → calls worker.Run()
    migrate.go                        # agentdock migrate (v1 → v2 config split)
    init.go                           # agentdock init app / init worker

  app/
    go.mod                            # module github.com/Ivantseng123/agentdock/app
    app.go                            # func Run(cfg *Config) error
    config/                           # AppConfig, applyAppDefaults, buildAppKoanf, preflight, validate, init
    bot/                              # workflow, result_listener, retry_handler, status_listener, parser
    slack/
    mantis/
    metrics/
    skill/                            # loader, watcher, npx (app-side only)

  worker/
    go.mod                            # module github.com/Ivantseng123/agentdock/worker
    worker.go                         # func Run(cfg *Config) error
    config/                           # WorkerConfig, applyWorkerDefaults, buildWorkerKoanf, preflight, validate, init, builtin_agents
    pool/                             # ex internal/worker
    agent/                            # ex internal/bot/agent.go (AgentRunner)
    prompt/                           # ex internal/worker/prompt.go (XML builder)

  shared/
    go.mod                            # module github.com/Ivantseng123/agentdock/shared
    queue/                            # Job, JobResult, PromptContext types + redis transport + mem store
    crypto/
    github/                           # shared GitHub client helpers
    logging/
    skill/                            # SkillPayload type only (no loader)
    config/                           # shared config types: RedisConfig, LoggingConfig, QueueConfig, GitHubConfig, RepoCacheConfig, AttachmentsConfig
    test/
      import_direction_test.go        # enforces module boundaries
```

Root `go.mod` uses `replace` to point at local sibling modules:

```
module github.com/Ivantseng123/agentdock

require (
    github.com/Ivantseng123/agentdock/app    v0.0.0
    github.com/Ivantseng123/agentdock/worker v0.0.0
    github.com/Ivantseng123/agentdock/shared v0.0.0
)
replace (
    github.com/Ivantseng123/agentdock/app    => ./app
    github.com/Ivantseng123/agentdock/worker => ./worker
    github.com/Ivantseng123/agentdock/shared => ./shared
)
```

`app/go.mod` and `worker/go.mod` each declare their own `replace` for `shared`.

### Dependency Rules

```
cmd/agentdock ─► app
              ─► worker
              ─► shared  (indirect via app/worker)

app   ─► shared
worker ─► shared

app   ✗ worker            (forbidden; enforced)
worker ✗ app              (forbidden; enforced)
shared ✗ app | worker     (forbidden; enforced)
```

Enforcement: `shared/test/import_direction_test.go` uses `go/packages` to scan all files in each module and assert no forbidden imports exist. Runs as part of standard `go test`.

### Config Field Allocation

| Field | App | Worker | Notes |
|---|:-:|:-:|---|
| `LogLevel`, `Logging` | ✓ | ✓ | Each has its own |
| `Slack` | ✓ | | |
| `Server` (HTTP port) | ✓ | | Worker has no HTTP |
| `GitHub` (token) | ✓ | ✓ | Each independent (can share value) |
| `Agents`, `ActiveAgent`, `Providers` | | ✓ | App does not execute agents; agent name not passed in Job payload |
| `Channels`, `ChannelDefaults`, `AutoBind`, `ChannelPriority` | ✓ | | Slack routing |
| `MaxThreadMessages`, `SemaphoreTimeout`, `RateLimit` | ✓ | | Slack handler |
| `Mantis` | ✓ | | |
| `Prompt` (goal, output_rules, language, allow_worker_rules) | ✓ | | App assembles `Job.PromptContext`; `AllowWorkerRules` passed as bool to worker via Job payload |
| `Worker.Count`, `Worker.Prompt.ExtraRules` | | ✓ | |
| `Attachments` | ✓ | | App downloads from Slack to temp dir; worker consumes via Redis AttachmentBus |
| `SkillsConfig` | ✓ | | App loads skills → `Job.Skills` payload; worker materializes them in cloned repo |
| `RepoCache` | ✓ | ✓ | Each side has its own cache dir (different processes) |
| `Queue` (timeouts) | ✓ | ✓ | Watchdog on app, pool on worker; values should match |
| `Redis` | ✓ | ✓ | Same Redis, each side configures its own connection |
| `SecretKey` | ✓ | ✓ | Values must match (app writes beacon, worker verifies) |
| `Secrets` | ✓ | ✓ | App encrypts; worker decrypts; worker config may override |

### Load Flow Split

Per-scope load helpers:

```go
// app/config/load.go
func BuildAppKoanf(cmd *cobra.Command, configPath string) (*Config, *koanf.Koanf, *koanf.Koanf, DeltaInfo, error)
func LoadAndStash(cmd *cobra.Command, configPath string) error
func Validate(cfg *Config) error
func RunPreflight(cfg *Config) (map[string]any, error)
func AppDefaultsMap() map[string]any
func AppEnvOverrideMap() map[string]any

// worker/config/load.go — symmetric
```

Env var allocation:

| Env | App | Worker |
|---|:-:|:-:|
| `SLACK_BOT_TOKEN`, `SLACK_APP_TOKEN`, `MANTIS_API_TOKEN` | ✓ | |
| `ACTIVE_AGENT`, `PROVIDERS` | | ✓ |
| `GITHUB_TOKEN`, `REDIS_ADDR`, `REDIS_PASSWORD`, `SECRET_KEY`, `AGENTDOCK_SECRET_*` | ✓ | ✓ |

CLI flags get sorted the same way: app subcommand owns Slack-related flags, worker subcommand owns agent/worker-count flags, and a small set of shared flags (e.g. `--redis-addr`) are added to both subcommands by their own registrar.

`cmd/agentdock/main.go` dispatches:

```go
func runAppCmd(cmd *cobra.Command, args []string) error {
    cfg, err := appconfig.LoadAndStash(cmd, appConfigPath)
    if err != nil { return err }
    return app.Run(cfg)
}
```

### Inmem Mode Handling

The `app` subcommand gains `--worker-config <path>` flag, used only when `Queue.Transport != "redis"`. In that case, after loading its own `AppConfig`, the app additionally loads the worker config and passes it to its embedded `LocalAdapter`:

```go
// app/app.go (inside Run)
if cfg.Queue.Transport != "redis" {
    workerCfgPath := resolveWorkerConfigPathForInmem(cfg, workerConfigFlag)
    wcfg, err := workerconfig.Load(workerCfgPath)
    if err != nil {
        return fmt.Errorf("inmem mode requires worker config: %w", err)
    }
    localAdapter := NewLocalAdapter(..., wcfg.Agents, wcfg.Worker.Count, wcfg.Worker.Prompt.ExtraRules, ...)
}
```

Redis mode ignores `--worker-config` entirely. This keeps app's own config free of agent-related fields while preserving inmem behavior.

### Migration Tool: `agentdock migrate`

```
$ agentdock migrate
Reading legacy config: /home/user/.config/agentdock/config.yaml
Detected schema version: v1 (single file)

Analysis:
  app-only fields:    slack, channels, channel_defaults, auto_bind, mantis, ...
  worker-only fields: agents, active_agent, providers, worker.count, ...
  shared fields:      github, redis, logging, secret_key, secrets, repo_cache, queue

Writing:
  /home/user/.config/agentdock/app.yaml       (18 fields)
  /home/user/.config/agentdock/worker.yaml    (12 fields)
Backing up original to:
  /home/user/.config/agentdock/config.yaml.v1.bak

Legacy key warnings:
  - workers.count → mapped to worker.count
  - max_concurrent (deprecated) → mapped to worker.count in worker.yaml
  - prompt.extra_rules (legacy) → NOT auto-migrated; decide manually
    whether it should go to app:prompt.output_rules or worker:worker.prompt.extra_rules

Done. See docs/MIGRATION-v2.md for deployment updates.
```

Implementation: `cmd/agentdock/migrate.go` reads legacy `Config` struct (retained as internal type during transition), routes fields to `AppConfig` / `WorkerConfig`, writes both files, creates `.v1.bak`.

Runtime legacy detection: if `-c` not given, both subcommands `os.Stat` the new default paths first; if absent and legacy `config.yaml` exists, they abort with a directed error message pointing at `agentdock migrate`. Never silently fall back to legacy schema.

New default paths:
- `agentdock app -c`: `~/.config/agentdock/app.yaml`
- `agentdock worker -c`: `~/.config/agentdock/worker.yaml`
- `agentdock app --worker-config`: `~/.config/agentdock/worker.yaml`

### `agentdock init` Restructure

- `agentdock init app -c app.yaml` — app starter config (interactive mode asks Slack tokens)
- `agentdock init worker -c worker.yaml` — worker starter config (interactive mode asks GitHub token, providers, active_agent, secret_key, redis)
- `agentdock init --all` — both at once

### Docs Restructure

Top-level `README.md` keeps the overall introduction (product positioning, "what is AgentDock", architecture diagram) and links to sub-READMEs. `app/` and `worker/` each get their own `README.md` covering only that module — there is no content overlap, just cross-links where relevant.

| File | Action |
|---|---|
| `README.md` / `.en.md` | Keep intro and architecture only; link to `app/README.md` and `worker/README.md` |
| `app/README.md` / `.en.md` | New — app-specific setup, deployment (K8s), configuration pointer |
| `worker/README.md` / `.en.md` | New — worker-specific setup, local usage, configuration pointer |
| `docs/configuration.md` / `.en.md` | Split into `configuration-app.md` and `configuration-worker.md`; original becomes index |
| `docs/MIGRATION-v2.md` / `.en.md` | New — legacy config migration, K8s ConfigMap update, `--worker-config` flag |
| `CLAUDE.md` | Update Landmines to reflect module structure and import rules |

### Testing Strategy

1. **Unit tests travel with source**: `internal/config/config_test.go` splits into `app/config/*_test.go` and `worker/config/*_test.go`; assertions unchanged, only imports updated. Same for `internal/bot/*_test.go`, `internal/worker/*_test.go`, etc.
2. **Import direction test** (new): `shared/test/import_direction_test.go` scans all files in app / worker / shared, asserts no forbidden imports.
3. **Migrate golden-file tests** (new): `cmd/agentdock/migrate_test.go` with fixtures in `cmd/agentdock/testdata/migrate/` (v1-full, v1-minimal, v1-with-legacy-keys inputs; expected-app/worker output pairs). Use `go-cmp` for deep equality.
4. **Test execution**: add `script/test-all.sh` running `go test ./...` in each of the four module roots. Add `.github/workflows/test.yml` (currently absent) invoking it on pull requests.

## Implementation Phases

Breaking changes (Phase 4+5) must ship together as one PR to avoid broken main; other phases can each be independent PRs.

### Phase 1 — Shared module (low risk)
- Rename root module `agentdock` → `github.com/Ivantseng123/agentdock`
- Introduce `shared/go.mod` with replace directive
- Move `internal/queue`, `internal/crypto`, `internal/logging`, `internal/github` into `shared/`
- Extract skill types (SkillPayload) into `shared/skill/`, leave loader in place
- Extract shared config types into `shared/config/`
- Update all import paths; run `go mod tidy` in both modules
- **End state**: binary behavior identical; two modules active

### Phase 2 — Worker module
- Introduce `worker/go.mod`
- Move `internal/worker/{pool,executor}`, `internal/bot/agent.go`, `internal/worker/prompt.go`, `internal/config/builtin_agents.go` into `worker/`
- Expose `worker.Run(cfg)` entry; wire `cmd/agentdock/worker.go` to call it
- **End state**: three modules (root, shared, worker); app code still in `internal/`; Config struct still legacy

### Phase 3 — App module
- Introduce `app/go.mod`
- Move `internal/slack`, `internal/mantis`, `internal/metrics`, the remaining `internal/bot/*` (workflow, listeners, parser, retry), and `internal/skill` loader/watcher/npx into `app/`
- Expose `app.Run(cfg)` entry; wire `cmd/agentdock/app.go` to call it
- **End state**: all four modules (root cmd, shared, app, worker); Config struct still legacy

### Phase 4 — Config split (high risk)
- Introduce `AppConfig` in `app/config/` and `WorkerConfig` in `worker/config/`
- Per-scope `applyDefaults`, `EnvOverrideMap`, `DefaultsMap`, `buildKoanf`, `validate`, `runPreflight`
- Change `app.Run` / `worker.Run` signatures to take split configs
- Add `--worker-config` flag for inmem mode in `app` subcommand
- Split `agentdock init` into `init app` / `init worker`
- **End state**: new load flow in place; legacy `config.yaml` no longer readable

### Phase 5 — Migration + user-facing cutover (ships with Phase 4)
- Implement `cmd/agentdock/migrate.go` with golden-file tests
- Change default config paths to `app.yaml` / `worker.yaml`
- Legacy-config detection with directed error message
- Write `docs/MIGRATION-v2.md`, split `configuration.md`, update root and new sub-READMEs
- **End state**: user-facing v2 release; breaking change announced in release notes

### Phase 6 — Cleanup
- Remove legacy `internal/*` directories that are now empty
- Add `import_direction_test.go`
- Add `script/test-all.sh` and `.github/workflows/test.yml`
- Update `CLAUDE.md` landmines section

### PR Breakdown

| PR | Phases | Approx commits |
|---|---|---|
| PR-1 | 1 | 8 |
| PR-2 | 2 | 6 |
| PR-3 | 3 | 7 |
| PR-4 | 4 + 5 | 14 |
| PR-5 | 6 | 4 |

PR-4 is large but cannot be split — Config schema change and migration tool must land together.

## Risks & Mitigation

| Risk | Likelihood | Impact | Mitigation |
|---|:-:|:-:|---|
| User migration loses data | Low | High | `.v1.bak` preserved; golden-file test exercises all canonical inputs; release notes emphasize migration |
| K8s production deployment breaks | Medium | High | Dev env validation before tag; MIGRATION-v2.md gives explicit ConfigMap split steps; rollback is `image: agentdock:v1.x` revert |
| Import path churn causes CI failure | Medium | Medium | `go mod tidy` per module after each phase; import-direction test |
| Inmem mode breaks | Medium | Medium | Integration test covers `--worker-config` path; clear error message if flag missing |
| Scope creep into issue #63 or other features | Medium | Medium | Issue #63 explicitly deferred; phase gates prevent feature additions during refactor |
| Release-please confused by branch strategy | Low | Low | Whole refactor on `refactor/v2-config-split` feature branch; merge-to-main triggers release-please once |

## Success Criteria (DoD)

1. `go test ./...` green in root, `app/`, `worker/`, `shared/`.
2. `release-validate.yml` snapshot build green.
3. `shared/test/import_direction_test.go` green (enforces boundaries).
4. `agentdock migrate` produces exact golden output for all three fixture inputs.
5. End-to-end manual validation:
    - Redis mode: `agentdock app -c app.yaml` + `agentdock worker -c worker.yaml` complete one triage round-trip.
    - Inmem mode: `agentdock app -c app.yaml --worker-config worker.yaml` completes one triage round-trip.
6. A user with a typical v1 `config.yaml` can follow `docs/MIGRATION-v2.md` and reach a working v2 setup.

## Follow-up (Not in this spec)

- Issue #63 `extra_args`: adds `WorkerConfig.Agents.<name>.ExtraArgs` field and a `{extra_args}` placeholder in `BuiltinAgents` args. Estimated 1-2 days post-refactor.
- `mergeBuiltinAgents` all-or-nothing partial-override fix (original #63 scope): deferred; may be revisited if users hit the trap.
- Agent CLI environment isolation (XDG / HOME redirect) for plugin-pollution defense: deferred as low-frequency concern; needs per-agent-CLI feasibility study.
- Issue #54 sub-issue #62 (worker takes over output / Slack response text): independent spec.
- Future git repository split (separate repos for app and worker): deferred until team or release cadence diverges.
