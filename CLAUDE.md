# Slack Issue Bot

Go service that creates GitHub issues from Slack emoji reactions, with AI-powered codebase triage.

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

## Project Structure

```
cmd/bot/main.go              # Entry point, wires deps, Socket Mode event loop
internal/
  config/config.go           # YAML config with env overrides, per-provider timeout
  bot/workflow.go            # Orchestrator: reaction → thread interaction → rejection → issue
  diagnosis/
    engine.go                # Agent loop: system prompt → LLM tool calls → execute → repeat
    tools.go                 # 6 tools: grep, read_file, list_files, read_context, search_code, git_log
    cache.go                 # In-memory response cache with TTL
    prompt.go                # System prompt builder with tool descriptions and language config
  llm/
    provider.go              # ConversationProvider interface, ChatFallbackChain, DiagnoseResponse
    cli.go                   # CLI provider (claude --print, etc.), stdin/args routing
    claude.go                # Anthropic Messages API with tool use support
    openai.go                # OpenAI-compatible API with tool use support
    ollama.go                # Ollama local LLM with tool use support
    prompt.go                # Legacy prompt helpers (kept for lite mode)
  slack/
    client.go                # PostMessage/PostSelector with thread_ts support
    handler.go               # Dedup, rate limiting, bounded concurrency
  github/
    issue.go                 # FormatIssueBody with GitHub file permalinks
    repo.go                  # RepoCache: full clone, fetch, branch list, checkout
```

## Key Design Decisions

### Triage Card (not full diagnosis)
LLM is instructed to NOT guess field names, variable names, or UI positions. Output is a short triage card:
- Max 10 related files with GitHub permalinks (configurable via max_files)
- Open questions for anything uncertain
- Confidence level (low/medium/high)
- No implementation suggestions — engineer decides

### Thread-Based Interaction
All bot messages (selectors, results, errors) reply in the original message's thread via `thread_ts`. Prevents channel noise when multiple issues are being created simultaneously.

### Rejection Mechanism
Bot refuses to create issue if ANY of: `files=0`, `open_questions>=3`, `confidence=low`. Replies in thread asking reporter to refine the description.

### Repo Context Reading
Before diagnosis, engine reads README.md, CLAUDE.md, agent.md (if they exist) from the repo. This helps LLM understand cross-repo relationships and suggest when an issue might involve another repo.

### Agent Loop Diagnosis
The LLM drives its own investigation via tool calls in a multi-turn conversation loop:
1. Engine sends system prompt + user message + available tools to LLM
2. LLM responds with tool calls (grep, read_file, list_files, etc.)
3. Engine executes tools locally and returns results as tool_result messages
4. Loop repeats until LLM emits a final JSON response (stop_reason=finish) or max_turns is reached
5. Engine forces a final answer if the turn limit is hit

This handles non-English messages naturally -- the LLM translates to code identifiers as part of its reasoning. Responses are cached (configurable TTL) to avoid redundant work.

### CLI Provider
`claude --print` with `{prompt}` in args (< 32KB) or stdin (large). Prompt goes via args OR stdin, never both. Default timeout 5 minutes.

### GitHub File Links
Issue body uses `[path](https://github.com/owner/repo/blob/branch/path)` format for clickable links.

## Lessons Learned

- **CLI timeout**: `claude --print` takes 1-3 min. Per-provider `timeout: 5m` required.
- **Slack `invalid_blocks`**: Don't combine `MsgOptionMetadata` with `MsgOptionBlocks`.
- **Slack events won't re-fire**: Must react to new messages when testing.
- **Full clone required**: Shallow clone can't list branches. Use `fetch --all --prune`.
- **Anthropic-compatible APIs**: Use `name: "claude"` in config for MiniMax etc.
- **Reporter tag**: Don't use `@username` — Slack/GitHub names differ. Plain text only.

## Testing

```bash
go test ./...   # 45 tests
```

## Build & Run

```bash
./run.sh
# or
go build -o bot ./cmd/bot/ && ./bot -config config.yaml
```
