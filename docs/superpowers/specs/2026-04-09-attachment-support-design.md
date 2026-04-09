# Attachment Support: xlsx + jpg/png Vision

## Summary

Add support for Slack message attachments so the diagnosis engine can leverage richer context:
- **xlsx**: parse into TSV text, inline into message (like existing text file handling)
- **jpg/png**: download bytes, send to LLM via vision API for AI triage analysis

Note: csv is already supported by the existing `isTextFile` handler — no changes needed.

## Motivation

Currently the bot only reads text-based attachments (txt, csv, json, etc.). Common Slack attachments like Excel reports and screenshots are reduced to `[附件: name](permalink)` — the LLM never sees their content. For a bug triage tool, screenshots and data files are often the most valuable context.

## Scope

### In scope
- xlsx file parsing (via excelize) with 200-row cap
- jpg/png image support via vision for Claude API, OpenAI API, and CLI providers
- Ollama fallback to text annotation for images
- Temp file cleanup for CLI provider
- Error handling with graceful fallback

### Out of scope
- pdf parsing
- ppt/docx parsing
- Image resize/compression
- Multi-page xlsx beyond row truncation

## Data Model

### Slack layer — `FetchedMessage`

```go
// slack/client.go

type ImageData struct {
    Name      string // "screenshot.png"
    MimeType  string // "image/png"
    Data      []byte // raw bytes
    Permalink string // Slack permalink (for fallback/issue body)
}

type FetchedMessage struct {
    Text   string      // existing text + inlined text/xlsx content
    Images []ImageData // jpg/png image bytes
}
```

`FetchMessage` returns `FetchedMessage` instead of `string`.

**Binary downloads**: `downloadBytes` uses `c.api.GetFile(url, buf)` with a `bytes.Buffer` (not `strings.Builder`). This reuses the existing Slack API auth — no separate HTTP call needed.

**Image permalinks in Text**: jpg/png images BOTH get extracted to `Images` (for LLM vision) AND keep a `[圖片: name](permalink)` annotation in `Text` (for GitHub issue body readability). This ensures the issue body always has image context even though image bytes are not passed to `IssueInput`.

### LLM layer — `Message` extension

```go
// llm/provider.go

type ImageContent struct {
    Name     string // for logging/fallback
    MimeType string
    Data     []byte
}

type Message struct {
    Role       string
    Content    string
    Images     []ImageContent // only first user message carries images
    ToolCalls  []ToolCall
    ToolCallID string
}
```

### Diagnosis layer — `DiagnoseInput` extension

```go
// diagnosis/engine.go

type DiagnoseInput struct {
    Type     string
    Message  string
    Images   []llm.ImageContent // new
    RepoPath string
    Keywords []string
    Prompt   llm.PromptOptions
}
```

## Data Flow

```
Slack reaction
  -> FetchMessage() returns FetchedMessage{Text, Images}
    -> pendingIssue stores Text + Images separately
      -> workflow.createIssue():
          - DiagnoseInput{Message: text, Images: images}
          - IssueInput{Message: text} (issue body doesn't need image bytes)
            -> RunLoop():
                - first user message: Message{Content: text, Images: images}
                - subsequent tool_result/assistant messages: no images
                  -> Provider.Chat():
                      - Claude: content blocks (text + image base64)
                      - OpenAI: content blocks (text + image_url data URI)
                      - CLI: write temp files + --file flag, defer cleanup
                      - Ollama: images -> [圖片: name] text fallback
```

Key decisions:
- Images are sent **only once** in the first user message of the agent loop, not repeated in subsequent turns (saves tokens).
- `ImageData` (Slack layer, has `Permalink`) is converted to `ImageContent` (LLM layer, no `Permalink`) in `workflow.createIssue()` before passing to `DiagnoseInput`.

### Token estimation

`estimateMessages` in `loop.go` must account for image tokens. Approximation: each image adds a flat 1600 tokens (Claude's cost for a typical screenshot). This is rough but sufficient for the 80% budget threshold check — prevents the agent loop from unknowingly blowing the token budget.

### Diagnosis cache key

`Engine.Diagnose` cache key (in `engine.go`) must include whether images are present. Simple approach: append `len(images)` to the existing cache key inputs. Same text + different images = different cache entry.

### System prompt update

Add a line to `AgentSystemPrompt` in `prompt.go` when images are present:
```
If screenshots or images are attached, use them to understand the reported behavior, error messages, or UI state. Reference what you see in the images when relevant to your triage.
```

## Files Changed

| File | Change |
|------|--------|
| `slack/client.go` | `FetchMessage` returns `FetchedMessage`; add `downloadBytes`, `parseXlsx` |
| `bot/workflow.go` | `pendingIssue` adds `Images` field; `HandleReaction` and `createIssue` pass images through |
| `diagnosis/engine.go` | `DiagnoseInput` adds `Images` |
| `diagnosis/loop.go` | `LoopInput` adds `Images`; first user message carries images |
| `llm/provider.go` | `Message` adds `Images`; new `ImageContent` struct |
| `llm/claude.go` | user message with images -> content blocks array |
| `llm/openai.go` | user message with images -> content blocks array |
| `llm/cli.go` | images -> write temp files + `--file` flags + defer cleanup |
| `llm/ollama.go` | images -> text annotation fallback |
| `llm/prompt.go` | conditional image guidance in system prompt |
| `go.mod` | add `github.com/xuri/excelize/v2` |

## Provider Implementation Details

### Claude API (`claude.go`)

Without images (unchanged):
```json
{"role": "user", "content": "text"}
```

With images:
```json
{"role": "user", "content": [
    {"type": "image", "source": {"type": "base64", "media_type": "image/png", "data": "..."}},
    {"type": "text", "text": "original message..."}
]}
```

Logic: `if len(m.Images) > 0` -> content blocks array, else keep existing string format.

### OpenAI API (`openai.go`)

With images:
```json
{"role": "user", "content": [
    {"type": "image_url", "image_url": {"url": "data:image/png;base64,..."}},
    {"type": "text", "text": "original message..."}
]}
```

### CLI (`cli.go`)

1. Collect all images from messages, write to temp files (`/tmp/slack-issue-bot-*.{ext}`)
2. `defer` cleanup: remove all temp files after Chat() returns
3. Add `--file /tmp/xxx.png` flags to CLI args (prepended before other args)
4. Final command: `claude --print --file /tmp/a.png --file /tmp/b.jpg "{prompt}"`

Note: `--file` is specific to `claude --print`. If the configured CLI tool does not support `--file` (e.g. a different CLI), images silently fall back to text annotation (same as Ollama). Detection: if the command name does not contain "claude", skip `--file` and append `[圖片: name]` to the prompt text instead.

### Ollama (`ollama.go`)

No API change. Images converted to text annotation during prompt assembly:
```
[圖片: screenshot.png]
```

## xlsx Handling

In `FetchMessage`, new `parseXlsx` function:

```go
func parseXlsx(data []byte, maxRows int) (string, error) {
    // Open with excelize
    // Iterate each sheet
    // Convert to TSV (tab-separated values)
    // Truncate at maxRows (200) with notice
}
```

**Multi-sheet behavior**: iterate all non-empty sheets. The 200-row cap is **per sheet**. Each sheet gets its own header in the output. Empty sheets are skipped.

Output format — TSV in code block (consistent with existing text file handling):
```
--- 附件: report.xlsx (Sheet: Sheet1, 顯示前 200/1048 行) ---
```
```
欄位A\t欄位B\t欄位C
值1\t值2\t值3
... [truncated, showing first 200 of 1048 rows]
```
```
--- (Sheet: Sheet2, 50 行) ---
```
```
...
```

TSV chosen over markdown table because: fewer tokens, no header separator needed, consistent with existing code block format.

## Limits

| Item | Limit | Rationale |
|------|-------|-----------|
| Image file size | 20 MB per image | Claude API limit |
| Image count | 5 per message | Token budget; excess images fallback to text annotation |
| xlsx row count | 200 rows per sheet | Token budget; user confirmed |
| Text file lines | 500 lines (existing) | Unchanged |

### Image type scope

Only **jpg/png** are sent to the LLM as vision content. Other image types already handled by `isImageFile` (gif, webp, svg) continue with the existing `[圖片: name](permalink)` text annotation — no change.

## Error Handling

| Scenario | Handling |
|----------|----------|
| Image download fails | log warn, fallback to `[圖片: name](permalink)` text |
| Image > 20MB | skip, fallback to text annotation |
| xlsx download fails | log warn, fallback to `[附件: name](permalink)` text |
| xlsx parse fails | log warn, fallback to `[附件: name](permalink)` text |
| CLI temp file write fails | log warn, skip that image, continue with others |
| Provider doesn't support vision | fallback to text annotation (Ollama) |

All errors are non-fatal — the workflow continues with whatever content was successfully extracted.

## Dependencies

- `github.com/xuri/excelize/v2` — xlsx parsing (well-maintained, MIT license)
- `encoding/base64` — stdlib, for image encoding

## Testing

- Unit test: `parseXlsx` with small xlsx fixture (normal, empty, multi-sheet, >200 rows)
- Unit test: `FetchedMessage` construction with mixed file types
- Unit test: Claude/OpenAI provider content blocks serialization with images
- Unit test: CLI provider temp file lifecycle (created, used in args, cleaned up)
- Unit test: Ollama fallback text for images
- Integration test: FetchedMessage with images flows through DiagnoseInput → RunLoop first message has images, subsequent messages do not
