# react2issue

Go service that turns chat reactions into structured GitHub issues with AI-powered codebase triage. Core value: lowering the barrier for non-engineers to create useful issues from Slack conversations.

## Architecture

```
Slack reaction → Socket Mode → Handler (dedup + rate limit + semaphore)
  → Workflow (repo/branch selection via thread buttons)
    → Diagnosis Engine:
        Pre-grep (original keywords) → Agent Loop (LLM picks tools) → Triage Card
        Tools: grep, read_file, list_files, read_context, search_code, git_log
    → Rejection/Degradation:
        confidence=low → reject (wrong repo)
        files=0 or questions>=5 → create issue WITHOUT triage section
        otherwise → create issue WITH full triage
    → GitHub Issue (clickable file links) → Post URL in thread
```

## Project Structure

```
cmd/bot/main.go              # Entry point, wires deps, Socket Mode event loop
internal/
  config/config.go           # YAML config with env overrides, per-provider timeout
  bot/workflow.go            # Orchestrator: reaction → thread interaction → rejection/degradation → issue
  diagnosis/
    engine.go                # Engine wrapper: agent loop + cache + lite mode (FindFiles)
    loop.go                  # Agent loop: pre-grep → LLM tool calls → forced finish → triage card
    tools.go                 # 6 tools: grep, read_file, list_files, read_context, search_code, git_log
    cache.go                 # In-memory response cache with TTL
  llm/
    provider.go              # ConversationProvider interface, ChatFallbackChain, DiagnoseResponse
    cli.go                   # CLI provider (claude --print, etc.), JSON-in-text tool simulation
    claude.go                # Anthropic Messages API with native tool use
    openai.go                # OpenAI-compatible API with function calling
    ollama.go                # Ollama local LLM with JSON-in-text tool simulation
    prompt.go                # Agent system prompt, tool descriptions, CLI tool suffix
  slack/
    client.go                # PostMessage/PostSelector/PostExternalSelector with thread_ts
    handler.go               # Dedup, rate limiting, bounded concurrency, messageDedup
  github/
    issue.go                 # FormatIssueBody with GitHub file permalinks
    repo.go                  # RepoCache: full clone, fetch, branch list, checkout
    discovery.go             # GitHub API repo discovery with cache (auto-bind)
```

## Key Design Decisions

### Tool positioning
This is a **structuring tool**, not a diagnosis tool. The core value is turning Slack conversations into formatted issues — AI triage is a bonus. Even when AI can't find related files, the issue (message + channel + reporter + repo) still has value.

### Rejection vs Degradation
- `confidence=low` → reject (likely wrong repo or completely irrelevant)
- `files=0` or `open_questions>=5` but confidence not low → create issue, skip AI triage section
- Normal results → create issue with full AI triage

### Agent Loop Diagnosis
Pre-grep with original keywords (catches non-English terms), then LLM-driven multi-turn loop:
1. LLM sees pre-grep results + available tools
2. LLM calls tools (grep, read_file, search_code, etc.)
3. Engine executes tools locally, returns results
4. Loop until LLM finishes or max_turns reached
5. Forced finish if turn limit hit

### Triage Card Output
LLM is instructed to NOT guess field names, variable names, or UI positions. Output:
- Each file listed once (no duplicates), with relevance description
- Direction gives high-level guidance, not specific code
- Existing implementations mentioned as "reference" not "instructions"
- Confidence level (low/medium/high)

### CLI Provider (JSON-in-text simulation)
Claude/OpenAI use native tool use APIs. CLI/Ollama providers embed tool schemas in the system prompt and parse `{"tool": "...", "args": {...}}` JSON from text responses. Safety net: if LLM outputs raw triage card JSON (no finish wrapper), parser detects `"summary"` field and treats it as finish.

### Thread-Based Interaction
All bot messages reply in the original message's thread via `thread_ts`.

### Auto-bind
Bot auto-registers when joining a channel. No static channel config needed.

## Lessons Learned

- **CLI timeout**: `claude --print` takes 1-3 min. Per-provider `timeout: 5m` required.
- **CLI ignores tool format**: Must strongly enforce JSON-only output in prompt with negative examples.
- **Pre-grep is essential**: LLM tends to translate to English before searching, missing non-English keyword hits. Pre-grep with original terms solves this.
- **Slack `invalid_blocks`**: Don't combine `MsgOptionMetadata` with `MsgOptionBlocks`.
- **Full clone required**: Shallow clone can't list branches. Use `fetch --all --prune`.
- **Reporter tag**: Don't use `@username` — Slack/GitHub names differ. Plain text only.

## Testing

```bash
go test ./...   # 76 tests
```

## Build & Run

```bash
./run.sh
# or
go build -o bot ./cmd/bot/ && ./bot -config config.yaml
```
