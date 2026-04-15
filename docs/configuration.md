# 設定

[English](configuration.en.md)

跑 `agentdock init -c /tmp/sample.yaml` 可產生含所有欄位的範本（加 `-i` 進入互動式填入）。完整 schema 見下方：

```yaml
auto_bind: true

channel_defaults:
  branch_select: true
  default_labels: ["from-slack"]

# Agent 設定
agents:
  claude:
    command: claude
    args: ["--print", "--output-format", "stream-json", "-p", "{prompt}"]
    timeout: 15m
    skill_dir: ".claude/skills"
    stream: true                      # 啟用即時事件追蹤
  opencode:
    command: opencode
    args: ["--prompt", "{prompt}"]
    timeout: 5m
    skill_dir: ".opencode/skills"

active_agent: claude
providers: [claude, opencode]

# Queue 設定
queue:
  capacity: 50                        # queue 上限
  transport: inmem                    # inmem | redis
  job_timeout: 20m                    # watchdog: 最大 job 生命週期
  agent_idle_timeout: 5m              # stream-json: 無 event 超時
  prepare_timeout: 3m                 # clone/setup 超時
  status_interval: 5s                 # worker 回報狀態頻率

workers:
  count: 3                            # worker pool 大小

# Redis 設定（transport: redis 時使用）
# redis:
#   addr: redis:6379
#   password: ""
#   db: 0

channel_priority:
  # C_INCIDENTS: 100                  # production incidents 優先
  default: 50

prompt:
  language: "繁體中文"
  extra_rules:
    - "列出所有相關的檔案名稱與完整路徑"
```

## Agent Stream 模式

Claude 支援 `--output-format stream-json`，啟用後可即時追蹤：
- 目前在用什麼 tool（Read, Bash, Grep...）
- 已讀了幾個檔案
- 已生成多少文字
- 花了多少錢（cost_usd, input/output tokens）

不支援 stream 的 agent（opencode, codex）只追蹤 PID + 存活狀態。

## Agent Skills

Skills 隨 Job 發送給 worker（`Job.Skills` 欄位），worker 在 clone 的 repo 裡寫入 skill 檔案（支援完整目錄樹：SKILL.md + examples + references），agent CLI 啟動時自動載入。不需要手動安裝。

```
agents/
  skills/
    triage-issue/
      SKILL.md           # triage skill — agent 分析 codebase 回傳結構化結果
  setup.sh               # local 開發：建 symlink（run.sh 自動呼叫）
```

### 動態 Skill 加載（NPX）

除了 baked-in skills，可透過獨立的 `skills.yaml` 設定從 npm registry 動態加載 skills：

```yaml
# skills.yaml（透過 k8s ConfigMap 掛載）
skills:
  triage-issue:
    type: local
    path: agents/skills/triage-issue

  code-review:
    type: remote
    package: "@team/skill-code-review"
    version: "latest"

cache:
  ttl: 5m    # NPX skill 的 cache 有效期
```

在 `config.yaml` 中指定路徑：
```yaml
skills_config: "/etc/agentdock/skills.yaml"
```

**特性：**
- **TTL cache + singleflight**：避免重複 fetch，cache 過期才重新拉取
- **兩層 fallback**：npx 失敗 → 用 cache 舊版 → 用 baked-in → 跳過
- **Negative cache**：失敗的 skill 在 TTL 內不重試
- **啟動預熱**：App 啟動時預先 fetch 所有 npx skills
- **Hot reload**：fsnotify 監控 skills.yaml，ConfigMap 更新後自動 reload
- **同名衝突 fail fast**：local 和 remote skill 同名時立即報錯，避免 silent override
- **檔案驗證**：單一 skill < 1MB，Job 總量 < 5MB，副檔名白名單，path traversal 防護

**NPM package convention：**
```
node_modules/@team/skill-code-review/
  skills/
    code-review/
      SKILL.md           # 必要
      examples/           # 選用
      references/         # 選用
```

私有 registry 需另行配置 `.npmrc`（透過 k8s Secret 掛載到 `/home/node/.npmrc`）。
