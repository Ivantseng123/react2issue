# Agent Loop Diagnosis Engine

## Goal

Replace the hardcoded 4-step diagnosis pipeline with an LLM-driven agent loop where the model decides which tools to use, in what order, and when it has enough information to produce a triage card.

## Architecture

### Current (pipeline)

```
keyword grep → LLM suggest terms → LLM pick from tree → LLM diagnose
```

Each step is a separate LLM call with a fixed role. The flow is always the same regardless of input.

### New (agent loop)

```
while turns < max_turns:
    LLM sees: system prompt + tool definitions + conversation history
    LLM replies: tool_call or finish
    if tool_call → execute tool → add result to conversation history
    if finish → return triage card
```

The LLM decides its own investigation strategy. A simple, clearly-written Chinese bug report might need only 2 turns (grep + finish). A vague report might use all 5 turns reading multiple files.

### Why this is better

- **Adaptive**: LLM can read README first, then decide what to grep based on project structure.
- **Fewer unnecessary calls**: If keyword grep hits immediately, LLM can read files and finish in 2-3 turns instead of always running 4 steps.
- **More capable**: LLM can check git log, do regex searches, read context docs — tools the pipeline couldn't use.
- **Simpler code**: One loop replaces four separate diagnosis functions.

## Tool Definitions

Six tools available to the LLM during diagnosis. The LLM prompt describes each tool's purpose so it knows when to use which:

### `grep`

Find which files mention a term. Use for broad discovery — "which files are related to X?"

- Input: `pattern` (string, required, max 200 chars), `max_results` (int, default 10)
- Behavior: `git grep -rli --count {pattern}` in the repo, scored by match frequency
- Output: List of matching file paths with hit counts
- Skips: `.min.js`, `vendor/`, `node_modules/`, `.lock`, `go.sum`, `target/`, `build/`, `.git/`

### `read_file`

Read the content of a specific file. Use after grep to examine a candidate file.

- Input: `path` (string, required), `max_lines` (int, default 200)
- Behavior: Read file from repo, cap at max_lines
- Output: File content with line numbers
- Error: File not found → return error message (LLM can try another file)

### `list_files`

List all files in the repo. Use when grep returns no results and you need to browse the file tree.

- Input: `pattern` (string, optional glob filter like `*.go` or `src/**/*.java`)
- Behavior: `git ls-files` with optional pattern filter, cap at 500 entries
- Output: File tree listing

### `read_context`

Read repo context documents (README, CLAUDE.md, etc.). Use to understand the repo's purpose, structure, and cross-repo relationships before investigating.

- Input: none
- Behavior: Read README.md, readme.md, CLAUDE.md, agent.md, AGENTS.md (first found, cap 100 lines each)
- Output: Concatenated context content
- Note: Context docs are NOT auto-loaded into the system prompt. The LLM must call this tool explicitly. This saves tokens when the LLM can diagnose without repo context (e.g. clear English-named files).

### `search_code`

Find exact code patterns with surrounding context. Use when you know roughly what to look for (function name, error message, etc.) and want to see the code around it.

- Input: `pattern` (string, required, max 200 chars), `file_pattern` (string, optional glob), `context_lines` (int, default 2)
- Behavior: `git grep -n -E {pattern}` with optional `-- {file_pattern}`, return matching lines with surrounding context
- Output: Matches with file path, line number, and surrounding lines
- Cap: Max 50 matches to prevent token explosion

### `git_log`

View recent commits. Use to check if related code was recently changed.

- Input: `count` (int, default 20), `path` (string, optional file path filter)
- Behavior: `git log --oneline -n {count} [-- {path}]`
- Output: Commit list (hash + message)

## Agent Loop Implementation

### Turn Structure

Each turn:
1. Build messages array: system prompt + tool definitions + all previous turns
2. Send to LLM provider (through FallbackChain)
3. Parse response — handle each case:
   - **Tool call(s)**: Execute first tool call only (ignore extras), append assistant message + tool result to messages. Increment turn counter.
   - **Finish (StopReason=finish with text content)**: Parse triage card JSON from text. If valid, return. If invalid JSON, use `ParseLLMTextResponse()` fallback parsing.
   - **Text only, no tool call, no valid triage card**: Treat as "thinking" — append as assistant message, continue loop. Still counts toward max turns.
   - **Unknown tool name**: Return error as tool result: `"Unknown tool: X. Available tools: grep, read_file, list_files, read_context, search_code, git_log"`. LLM can correct on next turn.
   - **Empty response**: Log warning, continue loop.
4. If turn limit reached: see "Turn Limit and Forced Finish" section.

### Message Format

System prompt includes:
- Role description (code triage assistant)
- Available tools with JSON schema
- Output format (triage card schema)
- Language preference from config
- Extra rules from config

Triage card schema (output of the agent loop):
```json
{
  "summary": "One sentence: what area of code is involved",
  "files": [{"path": "...", "line_number": 0, "description": "short phrase explaining relevance"}],
  "suggestions": ["What to investigate (max 2, bug type only)"],
  "open_questions": ["Clarifications only reporter/PM can answer"],
  "confidence": "low|medium|high",
  "complexity": "low|medium|high (feature type only)"
}
```

The `suggestions` field is used for bug type. The `complexity` field is used for feature type. Both are included in the schema; the LLM fills whichever is relevant based on the report type.

### Provider Adaptation

**Claude API (native tool use):** Use Anthropic's tool use format directly. Tools defined in API request, model returns `tool_use` content blocks with `id`. Tool results sent back as `tool_result` blocks with matching `tool_use_id`. When the model stops with `end_turn`, extract the triage card from the text content block (no `finish` tool needed — Claude naturally stops and outputs text).

**OpenAI (function calling):** Use OpenAI's function calling format. Tools defined as `functions` in the request. Model returns `tool_calls` array. Tool results sent as `tool` role messages. Model stops with `stop` finish reason.

**CLI / Ollama (JSON-in-text):** System prompt includes tool schemas as text. LLM outputs `{"tool": "...", "args": {...}}` JSON. Bot parses and executes. LLM outputs `{"tool": "finish", "result": {...}}` to end.

The provider interface changes from single-shot `Diagnose(req) → resp` to multi-turn:

```go
// Normalized stop reasons (each provider maps to these)
const (
    StopReasonToolUse = "tool_use"
    StopReasonFinish  = "finish"
)

type ToolCall struct {
    ID   string          // Tool use ID (required for Claude/OpenAI tool_result matching; generated UUID for CLI/Ollama)
    Name string          // Tool name
    Args json.RawMessage // Tool arguments as raw JSON
}

type Message struct {
    Role       string          // "assistant", "user", "tool_result"
    Content    string          // Text content
    ToolCalls  []ToolCall      // For assistant messages with tool calls
    ToolCallID string          // For tool_result messages (matches ToolCall.ID)
}

type ConversationProvider interface {
    Name() string
    // Chat sends messages and returns the model's response.
    // Each provider normalizes its native format to/from these common types.
    Chat(ctx context.Context, req ChatRequest) (ChatResponse, error)
}

type ChatRequest struct {
    SystemPrompt string
    Messages     []Message
    Tools        []ToolDef    // Tool schemas for the model
}

type ChatResponse struct {
    Content    string      // Text content (may coexist with ToolCalls)
    ToolCalls  []ToolCall  // Tool calls (zero or more)
    StopReason string      // StopReasonToolUse or StopReasonFinish
}
```

Each provider struct (ClaudeProvider, OpenAIProvider, etc.) implements both `Provider` (old single-shot) and `ConversationProvider` (new multi-turn). This is natural in Go — a struct can satisfy multiple interfaces.

### FallbackChain for Multi-Turn

The `FallbackChain` wraps `ConversationProvider`. Fallback operates **per-turn**, not per-loop:

- Each `Chat()` call within the agent loop goes through the fallback chain
- If provider A fails on turn 3, retry with provider A (up to MaxRetries)
- If provider A exhausts retries, fall back to provider B for that same turn
- The conversation history is re-serialized in the new provider's format (all providers use the normalized `Message` type, so this is transparent)
- The agent loop continues from where it left off — no restart

This means the loop can switch providers mid-conversation if one becomes unavailable. The normalized message types make this seamless.

### Backward Compatibility

The old `Provider` interface (`Diagnose`) is removed. Lite mode (`FindFiles`) uses grep only — no LLM calls needed. The `DiagnoseInput.Keywords` field is kept for lite mode's `FindFiles()` but ignored by the agent loop (the LLM decides its own search terms).

## Turn Limit and Forced Finish

- Default: 5 turns (configurable via `diagnosis.max_turns` in config.yaml)
- On the last allowed turn, system message appends: "This is your last turn. You MUST call the finish tool now with your best triage based on information gathered so far."
- If LLM still returns a tool call on the last turn: ignore it, send one more forced-finish message
- If LLM fails to produce valid JSON after forced finish: return a fallback response with whatever files were found during the loop

## Exponential Backoff

LLM API calls use exponential backoff on retryable errors:

- Retryable: HTTP 429, 500, 502, 503, 529, network timeout
- Non-retryable: HTTP 400, 401, 403 (fail immediately)
- Backoff: `min(base_delay * 2^attempt, max_delay)` with jitter
- `base_delay`: 1s, `max_delay`: 30s
- Respects `Retry-After` header if present
- Max retries: per-provider config (unchanged)

## Token Budgeting

Track approximate token usage per turn to avoid exceeding the model's context window:

- Estimate tokens: `len([]rune(text))` (character count, roughly 1:1 with tokens for CJK, slightly overestimates for English — acceptable for budget enforcement)
- Budget: configurable `diagnosis.max_tokens` (default 100,000)
- Before each LLM call: check if accumulated message tokens exceed 80% of budget
- If over budget: force finish on next turn
- Tool results that would push over budget are truncated with a note: `[truncated: {original_lines} lines → {kept_lines} lines]`

## Response Caching

Cache diagnosis results by message content hash:

- Key: SHA256 of `repo + branch + message_text + language + extra_rules`
- TTL: configurable `diagnosis.cache_ttl` (default 10m)
- Storage: in-memory map with mutex (same pattern as existing dedup)
- On cache hit: skip agent loop entirely, return cached `DiagnoseResponse`
- Cache is cleared when bot restarts (acceptable for this use case)

## Config Changes

```yaml
diagnosis:
  mode: "full"            # existing
  max_turns: 5            # NEW: agent loop turn limit
  max_tokens: 100000      # NEW: token budget per diagnosis
  cache_ttl: 10m          # NEW: diagnosis result cache TTL
  prompt:
    language: "繁體中文"    # existing
    extra_rules: [...]     # existing
```

## File Changes

### Modified

| File | Change |
|------|--------|
| `internal/diagnosis/engine.go` | Rewrite `Diagnose()` as agent loop. Remove `diagnoseWithFiles()`, `diagnoseWithTree()`, `llmSuggestSearchTerms()`, `llmPickFiles()`. Keep `FindFiles()` for lite mode. |
| `internal/llm/provider.go` | Replace `Provider` interface with `ConversationProvider`. Add `ChatRequest`, `ChatResponse`, `Message`, `ToolCall` types. Remove old `DiagnoseRequest`. Keep `DiagnoseResponse` (used as agent loop output). |
| `internal/llm/claude.go` | Implement `ConversationProvider` using Anthropic tool use API. |
| `internal/llm/cli.go` | Implement `ConversationProvider` using JSON-in-text tool call simulation. |
| `internal/llm/openai.go` | Implement `ConversationProvider` using OpenAI function calling format. |
| `internal/llm/ollama.go` | Implement `ConversationProvider` using JSON-in-text tool call simulation. |
| `internal/llm/prompt.go` | Update system prompt to describe available tools and agent behavior. |
| `internal/config/config.go` | Add `MaxTurns`, `MaxTokens`, `CacheTTL` to `DiagnosisConfig`. |
| `internal/bot/workflow.go` | Update `createIssue()` to post "正在分析..." before calling engine, update message when done. |
| `cmd/bot/main.go` | Pass new config fields to `diagnosis.NewEngine()`. |

### New

| File | Purpose |
|------|---------|
| `internal/diagnosis/tools.go` | Tool definitions: `GrepTool`, `ReadFileTool`, `ListFilesTool`, `ReadContextTool`, `SearchCodeTool`, `GitLogTool`. Each implements a `Tool` interface with `Name()`, `Schema()`, `Execute()`. |
| `internal/diagnosis/loop.go` | Agent loop implementation: `RunLoop(ctx, provider, tools, input) → DiagnoseResponse`. Handles turn counting, forced finish, token budgeting. |
| `internal/diagnosis/cache.go` | Diagnosis response cache with TTL. |

### Not Changed

| File | Reason |
|------|--------|
| `internal/slack/client.go` | Slack interaction unchanged |
| `internal/slack/handler.go` | Dedup/rate limiting unchanged |
| `internal/github/issue.go` | Issue formatting unchanged |
| `internal/github/discovery.go` | Repo discovery unchanged |
| `internal/github/repo.go` | Repo cache unchanged |

## Error Handling

- Tool execution error (e.g. file not found): Return error message to LLM as tool result. LLM can decide to try another approach.
- LLM returns invalid JSON: Retry parsing with the same fallback strategies as current `ParseLLMTextResponse()`. If still invalid, treat as text content and continue loop.
- All providers fail: `RunLoop` returns error. `workflow.go` catches this and falls back to lite mode (`FindFiles` grep-only, no LLM). Same as current behavior.
- Context timeout: Propagated through `context.Context`. Agent loop checks `ctx.Err()` between turns.

## Observability

Each turn logs via `slog.Info`:
- `"agent loop turn"`: turn number, tool called, tool result size (bytes), cumulative token estimate
- `"agent loop finished"`: total turns used, total token estimate, final confidence
- `"agent loop forced finish"`: when turn limit forces early completion
- `"agent loop cache hit"`: when a cached result is returned

This enables debugging and tuning `max_turns` based on real usage patterns.

## Testing

Existing engine tests (`engine_test.go`) will be replaced — the `Diagnose()` method signature and behavior change fundamentally. New tests:

- Unit tests for each tool (mock git commands via `exec.Command` wrapper)
- Unit test for agent loop with mock `ConversationProvider` (scripted multi-turn conversations)
- Unit test for forced finish behavior (turn limit reached)
- Unit test for ambiguous response handling (text-only, unknown tool, invalid JSON)
- Unit test for token budgeting truncation
- Unit test for response caching (hit/miss/expiry)
- Unit test for FallbackChain mid-conversation provider switch
- Integration test: full loop with real provider against a test repo
