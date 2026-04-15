# CLI 改用 spf13/cobra + Koanf 設定持久化 — Design

- **Date:** 2026-04-15
- **Status:** Draft v2 (post-grilling; awaiting user re-review)
- **Repo:** Ivantseng123/agentdock
- **Origin:** 對話需求 — `bot worker` 設定改用 cobra，所有可調整配置開 flag、merge 後寫回 `~/.config/agentdock/`
- **Related:** `2026-04-15-worker-interactive-preflight-design.md`（既有 worker preflight spec；本案複用該流程並抽出共用 prompt helpers）

## 摘要

把 AgentDock 的 CLI 從 stdlib `flag` 換成 spf13/cobra，提供 `app` / `worker` / `init` 三個子命令。所有可調整的 scalar 配置開出 flag。Config 載入用 `knadh/koanf/v2` 走四層 provider chain（default → config 檔 → env → flag），merge 後**有 delta 才**寫回 `~/.config/agentdock/config.yaml`，**env 不持久化**。Binary 名 `bot` → `agentdock`，hard break 至 **v1.0.0**。Migration 走獨立 doc `docs/MIGRATION-v1.md`。

## 動機

- **現況：** `cmd/bot/main.go:42-43` 與 `cmd/bot/worker.go:20-22` 只有 `-config` flag；其他配置只能改 YAML 或設 8 個 env vars。臨時調整 `workers.count`、`redis.addr` 之類得編 YAML 或前置 env，操作繁瑣。
- **目標：** 把所有 scalar 欄位開出 flag；首次設定後有 delta 就寫回 config 檔，下次啟動就記得；提供 `init` 子命令做一鍵 config 模板（含互動模式）。

## 決策摘要

腦力激盪 + 烤點 12 輪後的決議清單：

| # | 決策 | 來源 |
|---|---|---|
| D1 | Merge 順序 `flag > env > --config > default`，env 自成一層、**不被 save-back 持久化** | Q1 |
| D2 | 預設 config 路徑字面 `~/.config/agentdock/config.yaml`，跨平台一致（不走 `os.UserConfigDir()`） | Q2 |
| D3 | bot / worker 共用一份 config 檔（不分檔） | Q3 |
| D4 | Save-back 含 secrets，`chmod 0600` + atomic write | Q4 |
| D5 | `Channels` / `Agents` map 不開 flag，純走 config 檔 | Q5 |
| D6 | 保留 preflight 互動，merge 後仍缺值才 prompt | Q6 |
| D7 | Library: `cobra + knadh/koanf/v2`，hand-written flags | Approach 1 |
| D8 | 命令樹 `agentdock` (help) / `app` / `worker` / `init`；不帶 subcommand 印 help | Section A |
| D9 | 檔案放 `cmd/agentdock/` 平鋪，不開 sub-package | Section A |
| D10 | `init --interactive` 與 worker preflight 共用同一套 prompt helpers（`prompts.go`） | Section A |
| D11 | Binary 名 `bot` → `agentdock` | Section A |
| D12 | `DefaultsMap()` 從 `applyDefaults(empty Config)` round-trip 派生（一個 source of truth） | 烤點 1 |
| D13 | **Save-back delta-only**：preflight 有 prompt OR flag override OR 檔案不存在才寫 | 烤點 2 |
| D14 | App 也跑 preflight，比 worker 多問 Slack tokens（含 `auth.test` 驗） | 烤點 3 |
| D15 | 驗證走 cobra/pflag enum types + PreRunE `validate(cfg)`（一次列舉所有錯） | 烤點 4 |
| D16 | Built-in agents map 永遠 fallback；config 可 override 個別 agent | 烤點 5 |
| D17 | `init` 跟著 `--config` extension 出 YAML / JSON；無 / 未知 ext fallback YAML | 烤點 6 |
| D18 | Hard break 無 `bot` alias；版號 bump 至 **v1.0.0** | 烤點 7 |
| D19 | Migration guide 獨立檔 `docs/MIGRATION-v1.md` | 烤點 8 |
| D20 | 不加 `--config-format` flag（extension 已涵蓋） | 烤點 9 |
| D21 | `init --force -i` = wipe + 從零 prompt（不保留既有值） | 烤點 10 |
| D22 | 刪掉 `config.example.yaml`，`init` 是唯一 canonical | 烤點 11 |
| D23 | `-v` = `--version`（cobra 預設） | 烤點 12 |

## 命令樹

```
agentdock                         # root，不帶 subcommand → 印 help (D8)
├── app                           # 主 Slack bot (was: 原 ./bot 主程式)
├── worker                        # worker pool
└── init [-c PATH] [--force] [-i, --interactive]
                                  # 產 starter config 模板
```

`-h, --help` / `-v, --version` root 與三個 subcommand 都吃（`-v` 是 cobra 預設綁定 `--version`，D23）。

### Persistent flags（root，三個 subcommand 繼承）

- `-c, --config <path>`（預設 `~/.config/agentdock/config.yaml`）
- `--log-level`（pflag enum：debug / info / warn / error，D15）
- `--redis-addr`、`--redis-password`、`--redis-db`、`--redis-tls`
- `--github-token`
- `--mantis-base-url`、`--mantis-api-token`、`--mantis-username`、`--mantis-password`
- 所有 `Queue.*` scalar：`--queue-capacity`、`--queue-transport`（pflag enum：redis / inmem，D15）、`--queue-job-timeout`、`--queue-agent-idle-timeout`、`--queue-prepare-timeout`、`--queue-status-interval`
- 所有 `Logging.*` scalar：`--logging-dir`、`--logging-level`（pflag enum，D15）、`--logging-retention-days`、`--logging-agent-output-dir`
- `--repo-cache-dir`、`--repo-cache-max-age`
- `--attachments-store`、`--attachments-temp-dir`、`--attachments-ttl`
- `--workers`（= `Workers.Count`）
- `--active-agent`、`--providers`（comma-separated → `[]string`）
- `--skills-config`

### `app` 專屬 flags

- `--slack-bot-token`、`--slack-app-token`
- `--server-port`
- `--auto-bind`
- `--max-concurrent`、`--max-thread-messages`、`--semaphore-timeout`
- `--rate-limit-per-user`、`--rate-limit-per-channel`、`--rate-limit-window`

### `worker` 專屬 flags

無（全部繼承 root 即可）。

### 不開 flag 的欄位

`Channels map[string]ChannelConfig`、`Agents map[string]AgentConfig`、`Prompt`、`ChannelDefaults`、`ChannelPriority` map — 純走 config 檔。理由：nested map 用 flag 表達會醜（`--channels.<id>.repo=...`）且使用率低；走 YAML / JSON 直接編輯體驗最佳。`Agents` 額外有 D16 的 built-in fallback。

## Preflight 範圍 (D14)

兩個 subcommand 都跑 preflight，scope 不同：

```go
type PreflightScope string
const (
    ScopeApp    PreflightScope = "app"
    ScopeWorker PreflightScope = "worker"
)

func runPreflight(cfg *config.Config, scope PreflightScope, prompted map[string]any) error {
    // 共用：Redis address (PING)、GitHub token (/user 驗)、Providers (--version 驗)
    // ScopeApp 多問：Slack bot token (auth.test 驗)、Slack app token (xapp- 前綴驗)
}
```

`prompts.go` 共用所有 prompt helpers (`promptLine` / `promptHidden` / `promptYesNo` + `checkRedis` / `checkGitHubToken` / `checkAgentCLI` / `checkSlackToken`)。

`init -i` 統一問所有 5 件事（不分 scope）— 因為 init 寫的是共用 config，所以 Slack 也要問。

## `init` 子命令

```
agentdock init [-c, --config <path>] [--force] [-i, --interactive]
```

### 非互動模式（預設）

dump 一份 starter 檔到指定 / 預設 path：

- 所有 scalar 用 `applyDefaults` 真實預設值
- `agents:` block：D16 規定的內建三個（`claude` / `codex` / `opencode`），讓使用者知道能 override 哪些欄位
- `slack:` / `github:` / `redis:` 留空 + `# REQUIRED` 註解（YAML 模式才有）
- `channels:` 註解掉的範例 entry（YAML 模式才有）
- 寫檔 `chmod 0600`、atomic（先寫 `.tmp` 再 `os.Rename`）
- 寫完 stderr 印 `config written to <path>; edit secrets then run 'agentdock app'`，exit 0

### 互動模式 `-i`

跑與 preflight **同一套 prompt helpers**（`prompts.go`），問必填 5 件：

1. **Slack bot token**（hidden，`auth.test` 驗證）
2. **Slack app token**（hidden，至少驗 `xapp-` 前綴）
3. **GitHub token**（hidden，`/user` + `/user/repos` 驗）
4. **Redis address**（PING 驗）
5. **Providers**（從 hardcoded built-in agents map 數字選）

填完寫檔，與非互動模式相同的 chmod 0600 + atomic write。

### `--force` 行為 (D21)

- 無 `--force` + 檔已存在 → exit 1，stderr 印 `config already exists at <path>; pass --force to overwrite`
- `--force` → 直接覆寫，不備份；既有檔內容**完全忽略**
- `--force -i` → wipe + 從零 prompt（既有檔不被當預設值；想做局部修改請編檔或走 `agentdock app --field=val`）
- 目錄 `~/.config/agentdock/` 不存在 → `os.MkdirAll(dir, 0700)` 自動建

### Marshal 方式 (D17)

依 `--config` extension 切換：

```go
func pickInitFormat(path string) string {
    switch strings.ToLower(filepath.Ext(path)) {
    case ".json":         return "json"
    case ".yaml", ".yml": return "yaml"
    default:              return "yaml"  // 未知 / 無 ext → fallback YAML
    }
}
```

- **YAML 模式：** 用 `gopkg.in/yaml.v3` 直接 marshal，**不走 koanf**（要保留 `# REQUIRED`、範例 channel 註解）
- **JSON 模式：** 走 `koanf.Marshal(json.Parser())`，無註解（JSON 本來就不支援）

## 檔案結構

```
cmd/agentdock/                   # was cmd/bot/
  main.go                        # ~10 行：func main() { Execute() }
  root.go                        # rootCmd + persistent flags + version vars
  app.go                         # appCmd
  worker.go                      # workerCmd
  init.go                        # initCmd
  flags.go                       # 所有 flag 註冊 helper + flagToKey 映射表 + pflag enum types
  config.go                      # koanf load / merge / save-back / delta detect
  validate.go                    # validate(cfg) 跨欄位驗證 (D15)
  prompts.go                     # 互動 helpers（從 preflight.go 抽出）
  preflight.go                   # runPreflight (含 ScopeApp / ScopeWorker, D14)
  adapters.go                    # 從原 main.go 拆出 agentRunnerAdapter / repoCacheAdapter / slackPosterAdapter
  local_adapter.go               # 維持

internal/config/
  config.go                      # Config struct + applyDefaults + EnvOverrideMap() + DefaultsMap()
                                 # 移除 Load() / LoadDefaults() / applyEnvOverrides()
  builtin_agents.go              # NEW: BuiltinAgents map (D16)，從原 LoadDefaults 搬

docs/
  MIGRATION-v1.md                # NEW: v0.x → v1.0 升級指引 (D19)
```

### 連帶要改

- `Dockerfile` — `./cmd/bot/` → `./cmd/agentdock/`，binary `bot` → `agentdock`，entrypoint `agentdock app`
- `run.sh` — 同上
- `.github/workflows/*.yml` — release 流程裡 binary 名與路徑
- `README.md` — 使用方式 / 連結 `MIGRATION-v1.md`
- **刪除** `config.example.yaml`（D22；`init` 是唯一 canonical 來源）
- CHANGELOG（release-please 自動產，commit message 帶 `BREAKING CHANGE:` footer）

### 為什麼 `cmd/agentdock/` 而非直接放 `cmd/`

Go 慣例 `cmd/<binary>/<files>`（`kubectl` / `gh` / `hugo` / `prometheus`）。`go build ./cmd/agentdock/` 產生 `agentdock` binary。若未來新增第二個 binary（如 admin CLI），`cmd/<other>/` 路徑現成不用重構。

### 為什麼不開 sub-package（`cmd/agentdock/cmd/`）

cobra 文件預設那樣寫是 multi-binary 大專案的範例。AgentDock 一個 binary 一層 subcommand，平鋪比較好讀。

## Built-in Agents Fallback (D16)

`internal/config/builtin_agents.go` 維持 hardcoded `BuiltinAgents` map（從現有 `LoadDefaults` 搬）：

```go
var BuiltinAgents = map[string]AgentConfig{
    "claude":   {Command: "claude", Args: []string{"--print", "--output-format", "stream-json", "-p", "{prompt}"}, SkillDir: ".claude/skills", Stream: true},
    "codex":    {Command: "codex",   Args: []string{...}, SkillDir: ".codex/skills",   Stream: true},
    "opencode": {Command: "opencode", Args: []string{...}, SkillDir: ".opencode/skills"},
}
```

Runtime 在 koanf unmarshal 完 `cfg` 之後做 fallback merge：

```go
for name, agent := range BuiltinAgents {
    if _, exists := cfg.Agents[name]; !exists {
        if cfg.Agents == nil {
            cfg.Agents = map[string]AgentConfig{}
        }
        cfg.Agents[name] = agent
    }
}
```

Config 同名 entry 完全 override built-in（user 的 `claude.command = "/usr/local/bin/claude-canary"` 覆蓋整個 entry）。Built-in 只在 user 沒定義那個 name 時補上。

**新增 built-in agent**（如將來加 `kiro`）：只改 `BuiltinAgents` map；既有使用者升級後立即可用，不需要重跑 `init`。

`init` 仍把 built-in 寫進 starter file（教學價值），但 runtime 不依賴 file 完整性。

**Test：** `builtin_agents_test.go` 確保 `init` 寫出的 agents 區段跟 `BuiltinAgents` 內容一致（防 drift）。

## Validation 層次 (D15)

兩段式：

### 1. cobra/pflag enum types（flag 解析時擋）

對 closed-set 欄位用 `pflag.Var` + 自訂 enum type：

```go
type queueTransport string
func (q *queueTransport) String() string  { return string(*q) }
func (q *queueTransport) Type() string    { return "queue-transport" }
func (q *queueTransport) Set(v string) error {
    switch v {
    case "redis", "inmem":
        *q = queueTransport(v); return nil
    }
    return fmt.Errorf("must be one of [redis inmem]")
}
```

cobra 自動在 flag parse 時印 `invalid argument "foo" for "--queue-transport" flag` + usage、exit 2。涵蓋：

- `--queue-transport` ∈ {`redis`, `inmem`}
- `--log-level` / `--logging-level` ∈ {`debug`, `info`, `warn`, `error`}

### 2. PreRunE `validate(cfg) error`（merge 完跑跨欄位驗證）

```go
func validate(cfg *config.Config) error {
    var errs []string
    if cfg.Workers.Count < 1 {
        errs = append(errs, "workers.count must be >= 1")
    }
    if cfg.Queue.Capacity < 1 {
        errs = append(errs, "queue.capacity must be >= 1")
    }
    if cfg.RateLimit.PerUser < 0 || cfg.RateLimit.PerChannel < 0 {
        errs = append(errs, "rate_limit.per_* must be >= 0")
    }
    if cfg.Queue.JobTimeout <= 0 {
        errs = append(errs, "queue.job_timeout must be > 0")
    }
    // ... 其他範圍 / 格式 / 跨欄位規則
    if len(errs) > 0 {
        return fmt.Errorf("config validation failed:\n  %s", strings.Join(errs, "\n  "))
    }
    return nil
}
```

**所有錯一次列舉**（不 fail-fast）— 使用者一次看完所有問題，不用一輪一輪改。

### Schema 未知 key

koanf load 完，比對 `cfg` struct 的 yaml tags 可達 path 集合 vs koanf 看到的 keys，不在的印 `slog.Warn("unknown config key", ...)`，不 fatal。保留現有 v1 (`reactions`、`integrations`) 偵測精神，但走通用機制（任何未知 key 都警告）。

## Config 資料流

### 啟動序列（app / worker 共用）

```
1. cobra.Execute() → 解析 flags + 跑 PersistentPreRunE
2. PersistentPreRunE:
     a. 解析 --config 路徑（含 ~ 展開）
     b. buildKoanf(cmd) → 回 (cfg, kEff, kSave, deltaInfo)
     c. mergeBuiltinAgents(cfg)（D16）
     d. validate(cfg)（D15；錯一次列舉）
     e. preflight.Run(cfg, scope, prompted)（缺值 + interactive 才 prompt；scope 看 subcommand）
     f. saveConfig(kSave, path, prompted, deltaInfo)（delta-only，D13）
     g. cfg 塞進 cmd.Context()
3. RunE: 從 ctx 拿 cfg 跑主流程
```

`init` 是另一條短路徑：解析 path → 檢查存在 → (`-i` 才 prompt) → marshal + 寫檔 → exit。

### koanf 兩 instance（pseudo-Go）

```go
kEff  := koanf.New(".")   // effective config（給 runtime）
kSave := koanf.New(".")   // 給 save-back marshal

// 後 Load 蓋前面 → load 順序就是優先序低到高

// L0: defaults — 兩邊都載
kEff.Load(confmap.Provider(DefaultsMap(), "."), nil)
kSave.Load(confmap.Provider(DefaultsMap(), "."), nil)

// L1: --config 檔（YAML 或 JSON 看副檔名）— 兩邊都載
parser := pickParser(path)  // .yaml/.yml→yaml, .json→json
fileExisted := fileExists(path)
if fileExisted {
    kEff.Load(file.Provider(path), parser)
    kSave.Load(file.Provider(path), parser)
}

// L2: env — **只給 kEff**
kEff.Load(confmap.Provider(EnvOverrideMap(), "."), nil)

// L3: cobra flags（只 Changed 過的）→ 走顯式 flag→key 映射表 — 兩邊都載
flagMap := buildFlagOverrideMap(cmd)
kEff.Load(confmap.Provider(flagMap, "."), nil)
kSave.Load(confmap.Provider(flagMap, "."), nil)

// Unmarshal kEff 到 Config struct（用 yaml tag）
kEff.UnmarshalWithConf("", &cfg, koanf.UnmarshalConf{Tag: "yaml"})

// deltaInfo 給 save-back 判斷要不要寫
deltaInfo := DeltaInfo{
    FileExisted:     fileExisted,
    HadFlagOverride: len(flagMap) > 0,
}
```

### `DefaultsMap()` 派生 (D12)

```go
func DefaultsMap() map[string]any {
    var cfg Config
    applyDefaults(&cfg)
    data, _ := yaml.Marshal(&cfg)
    out := map[string]any{}
    yaml.Unmarshal(data, &out)
    return out
}
```

`applyDefaults` 是唯一 source of truth。新增欄位只改一處；條件邏輯（如 `MaxConcurrent → Workers.Count` fallback）天然支援。Marshal/unmarshal round-trip 成本可忽略（單次啟動）。

### Flag → koanf key 映射

posflag.Provider 預設拿 flag name 當 koanf key（`--redis-addr` → `redis-addr`），但 Config struct 用 yaml snake_case tag。`-` 與 `_` 對應規則必須**逐 flag 控制**：

- `--redis-addr` → `redis.addr`（struct boundary 用 dot）
- `--logging-agent-output-dir` → `logging.agent_output_dir`（boundary dot + snake_case 保留）
- `--log-level` → `log_level`（純 snake）
- `--rate-limit-per-user` → `rate_limit.per_user`

簡單字串替換做不到。用**顯式映射表** + 手工建 `map[string]any`：

```go
// flags.go — single source of truth
var flagToKey = map[string]string{
    "redis-addr":               "redis.addr",
    "redis-password":           "redis.password",
    "redis-db":                 "redis.db",
    "redis-tls":                "redis.tls",
    "github-token":             "github.token",
    "logging-dir":              "logging.dir",
    "logging-level":            "logging.level",
    "logging-retention-days":   "logging.retention_days",
    "logging-agent-output-dir": "logging.agent_output_dir",
    "log-level":                "log_level",
    "rate-limit-per-user":      "rate_limit.per_user",
    "rate-limit-per-channel":   "rate_limit.per_channel",
    "rate-limit-window":        "rate_limit.window",
    "queue-capacity":           "queue.capacity",
    "queue-job-timeout":        "queue.job_timeout",
    // ... 每個 flag 一行
}

func buildFlagOverrideMap(cmd *cobra.Command) map[string]any {
    out := map[string]any{}
    cmd.Flags().Visit(func(f *pflag.Flag) {
        key, ok := flagToKey[f.Name]
        if !ok {
            return  // skip --help / --version / --config / --force / --interactive
        }
        switch f.Value.Type() {
        case "string":          out[key], _ = cmd.Flags().GetString(f.Name)
        case "int":             out[key], _ = cmd.Flags().GetInt(f.Name)
        case "bool":            out[key], _ = cmd.Flags().GetBool(f.Name)
        case "duration":        out[key], _ = cmd.Flags().GetDuration(f.Name)
        case "stringSlice":     out[key], _ = cmd.Flags().GetStringSlice(f.Name)
        case "queue-transport", "log-level":
            out[key] = f.Value.String()  // pflag enum types
        }
    })
    return out
}
```

**維護成本：** 每加一個 flag 要更新 (1) flag 註冊（`flags.go` helper）跟 (2) `flagToKey` map。

**Test 涵蓋：** `flags_test.go` 補一個測試 walk Config struct yaml tag、確保每個 flag 對應的 key 真的存在於 struct 路徑（catch 漏字）。

### Path 解析

```
"/abs/path/foo.yaml"           → 原樣
"./relative.yaml"              → filepath.Abs
"~/.config/agentdock/x.yaml"   → 展開 ~ 為 os.UserHomeDir()
未指定                         → ~/.config/agentdock/config.yaml（字面）
```

字面 `~/.config/agentdock` 跨平台一致（D2）。Windows 下會變 `C:\Users\<u>\.config\agentdock\config.yaml` — 不漂亮但不在目標 OS。

## Save-back (D13 delta-only)

### 觸發條件

只有以下任一成立才寫；否則 skip：

- **A. preflight 有 prompt 到新值**（`prompted` map 非空）
- **B. 有 flag override 對應到 Config 欄位**（`flagMap` 非空）
- **C. config 檔不存在**（`!deltaInfo.FileExisted`，要創建）

日常 `agentdock app` 啟動 + 完整 config 檔 + 沒帶 flag → 不寫回 → **保留使用者手寫註解**。

### 內容比對保護

即使觸發條件成立，先 marshal kSave 跟現有檔內容做 byte 比較；相同則 skip（防 race / 多餘寫）。

### Save-back 內容

`kSave.Marshal(parser)` — default + config 檔 + flag overrides + preflight 互動填入；**不含 env layer**。

### Preflight 結果寫進 kSave

preflight 跑完後，把它互動填入的欄位 `kSave.Set("redis.addr", v)`、`kSave.Set("github.token", t)` 等，再 marshal。下次啟動 config 檔已含值，preflight 不會再問。

### 寫檔

```go
os.MkdirAll(filepath.Dir(path), 0700)
tmpPath := path + ".tmp"
os.WriteFile(tmpPath, data, 0600)
os.Rename(tmpPath, path)   // atomic 替換
```

每次 save 都重設 mode 為 `0600`（防止外部改成寬權限）。

### 錯誤策略

- save-back 失敗 → `slog.Warn("config save failed", ...)`，**不 fail 啟動**（runtime 已有 in-memory cfg）
- `chmod 0600` 失敗（rare）→ 同上 warn 繼續

### 已知 UX 陷阱：Env-derived 值不持久化

D1 規定 env 不進 kSave。實務上意味著：

- 用 `REDIS_PASSWORD=xxx agentdock worker` 啟動，第一次 OK；下次 unset env 後啟動 → Redis 連不上（password 從未進過 config 檔）
- 用 `GITHUB_TOKEN=xxx` 啟動同理；preflight 不問 token（env 已填值），但不寫回，下次沒 env 又會被抓出來重問

這是 D1 的有意設計（避免 secrets 因為一次帶 env 就被永久寫進檔）。**README、`agentdock --help` 跟 `MIGRATION-v1.md` 都要明確說：要永久設定 secrets 請走 `--config` 檔或 `agentdock init -i`，env 只是「本次 session」用。**

## 錯誤處理

| 情境 | 行為 |
|---|---|
| `--config` 沒指定 + 預設路徑檔不存在 | 繼續（用 defaults + env + flags + preflight 跑，啟動後 save-back 創建檔案） |
| `--config` 指定 + 檔不存在 | fail：`config file not found: <path>; run 'agentdock init -c <path>' first` |
| 檔存在但解析失敗 | fail，印 koanf 錯誤訊息（YAML 含 line number） |
| 副檔名不在 `.yaml/.yml/.json` | fail：`unsupported config format: .toml; only .yaml/.yml/.json supported` |
| Env 格式錯誤（如 `PROVIDERS=,,,`） | `EnvOverrideMap()` 內過濾空 token；極端情況 `slog.Warn` 不 fail |
| flag 型別錯誤（如 `--workers abc`） | cobra/pflag 自動 reject 印 usage，exit 2 |
| flag enum 值非法（如 `--queue-transport=foo`） | pflag enum 自動 reject 印 usage（D15），exit 2 |
| `validate(cfg)` 跨欄位錯（如 `--workers=0`） | PreRunE fail，列舉所有錯誤後 exit 1 |
| Schema 未知 key（如殘留 v1 `reactions:`） | `slog.Warn`，不 fatal |
| `~/.config/agentdock/` 目錄無法建（permission） | fail，明確指 path |
| Preflight Ctrl-C / EOF | fail，exit code 130（SIGINT 標準） |
| Signal handling（runtime） | 維持現狀 — `signal.Notify(SIGTERM, SIGINT)` 觸發 graceful shutdown |

## 與現有部署的相容性（Breaking Changes）

### 變更清單

1. Binary `bot` → `agentdock`，子命令必填（`agentdock app` / `agentdock worker`）
2. CLI flag `-config` → `-c, --config`（cobra 慣例不支援單 dash 長名）
3. **Env 優先序變了：** 原本 env 蓋過 YAML，新版 YAML 蓋過 env
4. 預設 config 路徑：原本當前目錄 `config.yaml`，新版 `~/.config/agentdock/config.yaml`
5. 廢除 `config.example.yaml`（D22；改用 `agentdock init`）

### 版號 (D18)

**v0.x → v1.0.0** hard break。release-please commit message 帶 `BREAKING CHANGE:` footer 自動 bump。

### Hard break，無 alias

- 不留 `bot` 子命令 alias
- 不留 `-config` 單 dash 長名 normalizer
- 不偵測舊路徑自動搬

### Migration guide (D19)

獨立檔 `docs/MIGRATION-v1.md`，內含：

- Before/after 命令對照表（`./bot -config X` → `agentdock app -c X`）
- Docker entrypoint diff
- docker-compose / systemd ExecStart 範例
- env 優先序變化警告（特別點出 secrets-via-env 不再持久化的陷阱）
- 範例：把 `/etc/agentdock/config.yaml` 留原地的最小修改 vs 搬到新預設位置

## 測試策略

### 新增 test files

```
cmd/agentdock/
  config_test.go         # koanf layering、save-back round-trip、env exclusion、delta-only 觸發
  flags_test.go          # flag 註冊、flagToKey 對應、Config struct path 驗證
  init_test.go           # init 非互動 snapshot、--force 行為、JSON / YAML extension 切換
  prompts_test.go        # 互動 helpers（stub stdin/stdout）
  preflight_test.go      # 既有 → 補 ScopeApp / ScopeWorker 區分、cobra integration
  validate_test.go       # validate(cfg) 各規則、多錯誤列舉
  config_path_test.go    # ~ 展開、abs/rel、parser 選擇

internal/config/
  config_test.go         # 既有 → 補 EnvOverrideMap()、DefaultsMap() round-trip
  builtin_agents_test.go # 確保 BuiltinAgents 跟 init 寫出的 agents 區段內容一致
```

### 重點覆蓋

1. **Layering 優先序** — 4 層各自獨立 + 兩兩疊加 + 全疊；scalar / bool / duration / `[]string` 各驗代表性欄位
2. **Save-back delta-only** — 無 flag 無 prompt → 不寫；有 flag → 寫；preflight 有 prompt → 寫；檔案不存在 → 寫
3. **Save-back round-trip** — load → mutate flag → save → reload → 預期相等（不含 env）
4. **Env exclusion** — 設 env、無 flag、save、reload without env → 預期得 default 不是 env 值
5. **Secrets persisted** — `--github-token=ghp_xxx` flag → save → reload → token 在檔
6. **chmod 0600** — save 後 `os.Stat` mode mask = 0600
7. **Atomic write** — mock disk full / 寫到一半失敗，原檔不損
8. **Path resolution** — `~/.config/...` 在不同 `HOME` 下展開正確
9. **Format detection** — `.yaml` / `.yml` / `.json` round-trip
10. **`init` 非互動 YAML** — snapshot 比對輸出（含 `# REQUIRED` 註解）
11. **`init` 非互動 JSON** — extension `.json` → 純 JSON 無註解
12. **`init --interactive`** — stub stdin，驗最終 marshal
13. **`init --force -i`** — 既存檔被覆寫（驗內容 + mode）；既存值不被當預設
14. **Preflight scope** — `ScopeApp` 問 Slack tokens；`ScopeWorker` 不問
15. **Validation** — `--workers=0`、`--queue-transport=foo`、多錯誤一次列舉
16. **Built-in agents fallback** — config 缺 `kiro` 但 BuiltinAgents 有 → 最終 cfg 含
17. **Built-in vs init drift** — 兩者 agents 內容比對相等

### Out-of-scope

- cobra / pflag 自身解析（信任上游）
- koanf 自身 provider 機制（信任上游）
- Slack / GitHub / Redis 連線本身（已有 `checkRedis` / `checkGitHubToken`，整合測由現有 preflight test 涵蓋）

## 實作分階段（給 writing-plans 用的提示）

1. **Refactor 不換語意：** 搬 `cmd/bot/` → `cmd/agentdock/`；抽 `EnvOverrideMap()` / `DefaultsMap()` / `BuiltinAgents` helpers；保留現有 stdlib flag 路徑能 build 過、test 過
2. **加 cobra 框架：** `root` + `app` + `worker` + `init` 骨架，flags 暫接老 `Load()` — still works
3. **接 koanf：** 取代 `Load()` 為兩 instance 流程；built-in agents merge；preflight 改 PreRunE，加 ScopeApp / ScopeWorker 區分
4. **加 validation：** pflag enum types + `validate(cfg)` PreRunE
5. **加 `init` 含 `--interactive`，** 共用 prompt helpers，extension-based marshal
6. **Save-back 串起來：** delta-only + preflight 結果 → kSave + atomic + chmod
7. **改 Dockerfile / run.sh / workflows / README，** 刪 `config.example.yaml`，寫 `docs/MIGRATION-v1.md`
8. **補測試**

每階段都該獨立 build + 跑現有 150 個 test。

## 不在範圍內

- Config hot reload（現有 `skill.watcher` 是另一回事）
- 多 profile / named config 支援（後續可再開單獨 spec）
- `bot` binary alias / 舊 flag normalize 等向後相容 hack
- Schema migration（既有 YAML schema 不變；新增 schema 版本欄位也不在範圍）
- `--config-format` flag (D20，extension 已涵蓋)

## 真正未決

烤點 1-12 已涵蓋所有腦力激盪 / 烤點階段浮出的決策。剩下純 impl detail（cobra / koanf 版本 pin、flag 短旗 cosmetic、`os.UserHomeDir()` 失敗 fallback、`Queue.Register(WorkerInfo)` 是否實作等）留給 writing-plans / 實作階段判斷。
