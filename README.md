# Slack Issue Bot

Slack emoji reaction triggers automatic GitHub issue creation with AI-powered codebase triage.

## How It Works

1. Someone posts a bug report or feature request in a Slack channel
2. A team member reacts with a configured emoji (e.g., `:bug:` or `:rocket:`)
3. The bot replies **in the message thread**:
   - Shows repo selector (if multiple repos configured)
   - Shows branch selector (if enabled)
   - Clones/pulls the GitHub repo
   - Reads repo context docs (README.md, CLAUDE.md, agent.md)
   - Agent loop diagnosis: LLM drives tool calls (grep, read_file, list_files, etc.) in a multi-turn conversation
   - **Rejection check** — if the report is too vague, replies asking for clarification instead of creating an issue
   - Creates a GitHub issue with clickable file links and posts the URL in thread

## Features

- **Thread-based interaction** — all bot messages stay in the original message's thread
- **Multi-repo support** — one channel can map to multiple repos with button selector
- **Branch selection** — optionally pick which branch to analyze
- **Cross-repo awareness** — reads README/CLAUDE.md/agent.md to understand repo context and hint at cross-repo relationships
- **Rejection mechanism** — refuses to create issues when the report is too vague (no related files, too many unknowns, or low confidence)
- **GitHub file links** — file references in issues are clickable links to the actual source
- **LLM fallback chain** — multiple providers with per-provider retry and timeout
- **CLI provider** — use your own AI subscription (Claude Max, etc.) with zero API cost
- **Lite mode** — grep-only triage with zero LLM cost
- **Rate limiting** — per-user and per-channel throttling

## Prerequisites

| Item | How to Get |
|------|-----------|
| Go 1.22+ | [go.dev/dl](https://go.dev/dl/) |
| Slack App | [api.slack.com/apps](https://api.slack.com/apps) |
| GitHub PAT | GitHub Settings > Developer settings > Personal access tokens |
| LLM Provider | CLI (Claude Max) / API key (Anthropic/OpenAI) / Ollama (free) |

### Slack App Setup

1. Create a new app at [api.slack.com/apps](https://api.slack.com/apps)
2. **OAuth & Permissions** — add Bot Token Scopes:
   - `reaction_read`, `channels:history`, `chat:write`, `users:read`, `channels:read`
3. **Socket Mode** — enable it
4. **Basic Information** — generate an App-Level Token with scope `connections:write` (gives you the `xapp-` token)
5. **Event Subscriptions** — subscribe to `reaction_added` bot event
6. **Install to Workspace** — copy the `xoxb-` Bot Token

## Quick Start

```bash
cp config.example.yaml config.yaml
# Edit config.yaml with your tokens

# Run
go run ./cmd/bot/
# or
./run.sh
```

## Configuration

See `config.example.yaml` for all options. Key sections:

```yaml
channels:
  C05XXXXXX:
    repos:                          # Multiple repos → button selector in Slack
      - "org/backend"
      - "org/frontend"
    branch_select: true             # Show branch picker
    default_labels: ["from-slack"]

llm:
  providers:
    - name: "cli"
      command: "claude"             # Uses Claude Code CLI (Max plan)
      args: ["--print", "{prompt}"]
      timeout: 5m                   # CLI needs more time than API
      max_retries: 3

    - name: "claude"                # API fallback
      api_key: "sk-ant-..."
      model: "claude-sonnet-4-20250514"
      base_url: "https://api.anthropic.com"
      max_retries: 3
  timeout: 60s                      # Global default (per-provider overrides this)

diagnosis:
  mode: "full"                      # "full" (uses LLM) or "lite" (grep only)
  max_turns: 5                      # Max agent loop iterations
  max_tokens: 100000                # Max tokens per LLM call
  cache_ttl: 10m                    # Response cache TTL (0 = no caching)
  prompt:
    language: "繁體中文"
    extra_rules:
      - "列出所有相關的檔案名稱與完整路徑"
```

### Diagnosis Modes

| Mode | LLM Cost | What Happens |
|------|----------|-------------|
| `full` | tokens per trigger | Agent loop: LLM calls tools (grep, read_file, etc.) in multi-turn conversation until diagnosis complete |
| `lite` | **0 tokens** | Grep only, creates issue with file references for engineer's own AI |

### Rejection Mechanism

In `full` mode, the bot will **not** create an issue if any of these conditions are met:

| Condition | Meaning |
|-----------|---------|
| `related files = 0` | No relevant code found |
| `open_questions >= 3` | Too many unknowns — report is too vague |
| `confidence = low` | LLM judges the report doesn't relate to this repo |

Instead, the bot replies in thread asking the reporter to refine their description.

### CLI Provider

Use your own AI subscription instead of API keys:

```bash
# Install & login (one time)
npm install -g @anthropic-ai/claude-code
claude /login

# Configure in config.yaml:
# - name: "cli"
#   command: "claude"
#   args: ["--print", "{prompt}"]
#   timeout: 5m
```

### Environment Variable Overrides

```bash
export SLACK_BOT_TOKEN="xoxb-..."
export SLACK_APP_TOKEN="xapp-..."
export GITHUB_TOKEN="ghp_..."
export LLM_CLAUDE_API_KEY="sk-ant-..."
```

## Issue Output Example

```markdown
**Channel:** #backend-bugs | **Reporter:** Ivan Tseng

> 再保系統分保結果畫面，Item資料新增顯示出單單位欄位

### AI Triage

分保結果的 item 視角表格可能需要新增欄位，相關邏輯在 Result.vue。

### Related Files

- [`src/pages/ceding/Result.vue`](https://github.com/org/repo/blob/main/src/pages/ceding/Result.vue) — 分保結果 item 視角表格
- [`src/pages/ceding/cedingResult.vue`](https://github.com/org/repo/blob/main/src/pages/ceding/cedingResult.vue) — 分保結果父頁面

### Direction

- 確認後端 API 是否已回傳該欄位

### Needs Clarification

- 「出單單位(通訊處)」對應的後端欄位名稱未知
```

## Testing

```bash
go test ./...   # 45 tests
```

## Docker

```bash
docker build -t slack-issue-bot .
docker run -v $(pwd)/config.yaml:/config.yaml slack-issue-bot
```

## Architecture

```
Slack reaction → Socket Mode → Handler (dedup + rate limit + semaphore)
  → Workflow (repo/branch selection via thread buttons)
    → Diagnosis Engine (agent loop):
        System prompt + tools → LLM calls tools → engine executes → results back
        Tools: grep, read_file, list_files, read_context, search_code, git_log
        Loop runs up to max_turns; response cache avoids duplicate work
    → Rejection check (files=0, questions>=5, confidence=low)
    → GitHub Issue (clickable file links) → Post URL in thread
```

## License

MIT
