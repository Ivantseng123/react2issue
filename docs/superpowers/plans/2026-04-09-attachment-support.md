# Attachment Support (xlsx + jpg/png Vision) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Enable the bot to read xlsx file content and send jpg/png images to the LLM via vision APIs during AI triage.

**Architecture:** `FetchMessage` returns a `FetchedMessage` struct (text + images). Images flow through `DiagnoseInput` → `RunLoop` → first user `Message` → each provider serializes to its own API format (Claude content blocks, OpenAI image_url, CLI --file, Ollama text fallback). xlsx is parsed to TSV and inlined into the text field.

**Tech Stack:** Go, excelize v2 (xlsx), Anthropic Messages API, OpenAI Chat Completions API, claude CLI --file

**Spec:** `docs/superpowers/specs/2026-04-09-attachment-support-design.md`

---

## File Structure

| File | Responsibility |
|------|---------------|
| `internal/llm/provider.go` | `ImageContent` struct, `Images` field on `Message` |
| `internal/slack/client.go` | `FetchedMessage`, `ImageData`, `downloadBytes`, `isVisionImage`, refactored `FetchMessage` |
| `internal/slack/xlsx.go` | `parseXlsx` function (isolated for testability) |
| `internal/slack/xlsx_test.go` | xlsx parsing tests |
| `internal/diagnosis/engine.go` | `Images` on `DiagnoseInput`, cache key update |
| `internal/diagnosis/loop.go` | `Images` on `LoopInput`, token estimation, first message images |
| `internal/diagnosis/loop_test.go` | Token estimation + first-message-only image tests |
| `internal/llm/prompt.go` | Conditional image guidance in system prompt |
| `internal/llm/claude.go` | Content blocks array for user messages with images |
| `internal/llm/claude_test.go` | (new) Claude message serialization tests |
| `internal/llm/openai.go` | Content blocks array for user messages with images |
| `internal/llm/openai_test.go` | (new) OpenAI message serialization tests |
| `internal/llm/cli.go` | Temp file management + `--file` flags |
| `internal/llm/cli_test.go` | CLI args with images tests |
| `internal/llm/ollama.go` | Image text fallback in prompt assembly |
| `internal/bot/workflow.go` | `Images` on `pendingIssue`, plumbing through pipeline |

---

### Task 1: Add excelize dependency

**Files:**
- Modify: `go.mod`

- [ ] **Step 1: Add excelize**

```bash
cd /Users/ivantseng/local_file/slack-issue-bot && go get github.com/xuri/excelize/v2
```

- [ ] **Step 2: Verify build**

```bash
go build ./...
```

Expected: success (no code uses it yet, but the module is available).

- [ ] **Step 3: Commit**

```bash
git add go.mod go.sum
git commit -m "deps: add excelize/v2 for xlsx parsing"
```

---

### Task 2: Add ImageContent struct and extend Message

**Files:**
- Modify: `internal/llm/provider.go`

- [ ] **Step 1: Add ImageContent and extend Message**

In `internal/llm/provider.go`, add `ImageContent` struct after the existing `FileRef` struct, and add `Images` field to `Message`:

```go
type ImageContent struct {
	Name     string // original filename, for logging/fallback
	MimeType string // "image/png", "image/jpeg"
	Data     []byte // raw image bytes
}
```

Add `Images []ImageContent` field to the `Message` struct, after the `Content` field:

```go
type Message struct {
	Role       string         // "assistant", "user", "tool_result"
	Content    string
	Images     []ImageContent // vision images (only first user message)
	ToolCalls  []ToolCall
	ToolCallID string // For tool_result messages
}
```

- [ ] **Step 2: Verify build and existing tests pass**

```bash
go build ./... && go test ./...
```

Expected: all 76 tests pass — this is a struct addition, no behavior change.

- [ ] **Step 3: Commit**

```bash
git add internal/llm/provider.go
git commit -m "feat: add ImageContent struct and Images field to Message"
```

---

### Task 3: Implement xlsx parsing (TDD)

**Files:**
- Create: `internal/slack/xlsx.go`
- Create: `internal/slack/xlsx_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/slack/xlsx_test.go`:

```go
package slack

import (
	"os"
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"
)

// createTestXlsx creates a minimal xlsx file in a temp dir and returns the bytes.
func createTestXlsx(t *testing.T, sheets map[string][][]string) []byte {
	t.Helper()
	f := excelize.NewFile()
	first := true
	for name, rows := range sheets {
		if first {
			f.SetSheetName("Sheet1", name)
			first = false
		} else {
			f.NewSheet(name)
		}
		for i, row := range rows {
			for j, cell := range row {
				cellName, _ := excelize.CoordinatesToCellName(j+1, i+1)
				f.SetCellValue(name, cellName, cell)
			}
		}
	}
	tmp := t.TempDir() + "/test.xlsx"
	if err := f.SaveAs(tmp); err != nil {
		t.Fatalf("save xlsx: %v", err)
	}
	data, err := os.ReadFile(tmp)
	if err != nil {
		t.Fatalf("read xlsx: %v", err)
	}
	return data
}

func TestParseXlsx_SingleSheet(t *testing.T) {
	data := createTestXlsx(t, map[string][][]string{
		"Data": {
			{"Name", "Age"},
			{"Alice", "30"},
			{"Bob", "25"},
		},
	})

	result, err := parseXlsx(data, 200)
	if err != nil {
		t.Fatalf("parseXlsx failed: %v", err)
	}
	if !strings.Contains(result, "Alice") {
		t.Errorf("expected result to contain 'Alice', got:\n%s", result)
	}
	if !strings.Contains(result, "Sheet: Data") {
		t.Errorf("expected sheet name in header, got:\n%s", result)
	}
}

func TestParseXlsx_Truncation(t *testing.T) {
	var rows [][]string
	for i := 0; i < 300; i++ {
		rows = append(rows, []string{"row", "data"})
	}
	data := createTestXlsx(t, map[string][][]string{"Big": rows})

	result, err := parseXlsx(data, 200)
	if err != nil {
		t.Fatalf("parseXlsx failed: %v", err)
	}
	if !strings.Contains(result, "truncated") {
		t.Errorf("expected truncation notice, got:\n%s", result)
	}
	// Count data lines (non-header, non-empty).
	lines := strings.Split(result, "\n")
	dataLines := 0
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l != "" && !strings.HasPrefix(l, "---") && !strings.HasPrefix(l, "```") && !strings.Contains(l, "truncated") {
			dataLines++
		}
	}
	if dataLines > 200 {
		t.Errorf("expected at most 200 data lines, got %d", dataLines)
	}
}

func TestParseXlsx_MultiSheet(t *testing.T) {
	data := createTestXlsx(t, map[string][][]string{
		"Sheet1": {{"A", "B"}, {"1", "2"}},
		"Sheet2": {{"X", "Y"}, {"3", "4"}},
	})

	result, err := parseXlsx(data, 200)
	if err != nil {
		t.Fatalf("parseXlsx failed: %v", err)
	}
	if !strings.Contains(result, "Sheet: Sheet1") {
		t.Errorf("expected Sheet1 header")
	}
	if !strings.Contains(result, "Sheet: Sheet2") {
		t.Errorf("expected Sheet2 header")
	}
}

func TestParseXlsx_EmptySheet(t *testing.T) {
	data := createTestXlsx(t, map[string][][]string{
		"HasData": {{"A"}, {"1"}},
		"Empty":   {},
	})

	result, err := parseXlsx(data, 200)
	if err != nil {
		t.Fatalf("parseXlsx failed: %v", err)
	}
	if strings.Contains(result, "Sheet: Empty") {
		t.Errorf("empty sheet should be skipped")
	}
	if !strings.Contains(result, "Sheet: HasData") {
		t.Errorf("non-empty sheet should be present")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./internal/slack/ -run TestParseXlsx -v
```

Expected: FAIL — `parseXlsx` not defined.

- [ ] **Step 3: Implement parseXlsx**

Create `internal/slack/xlsx.go`:

```go
package slack

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/xuri/excelize/v2"
)

const defaultMaxXlsxRows = 200

// parseXlsx reads xlsx bytes and returns a TSV text representation.
// Each non-empty sheet gets a header line. Rows are capped at maxRows per sheet.
func parseXlsx(data []byte, maxRows int) (string, error) {
	if maxRows <= 0 {
		maxRows = defaultMaxXlsxRows
	}

	f, err := excelize.OpenReader(bytes.NewReader(data))
	if err != nil {
		return "", fmt.Errorf("open xlsx: %w", err)
	}
	defer f.Close()

	var sb strings.Builder
	for _, sheet := range f.GetSheetList() {
		rows, err := f.GetRows(sheet)
		if err != nil {
			continue
		}
		if len(rows) == 0 {
			continue
		}

		totalRows := len(rows)
		truncated := totalRows > maxRows

		if truncated {
			sb.WriteString(fmt.Sprintf("--- (Sheet: %s, showing first %d of %d rows) ---\n", sheet, maxRows, totalRows))
		} else {
			sb.WriteString(fmt.Sprintf("--- (Sheet: %s, %d rows) ---\n", sheet, totalRows))
		}

		sb.WriteString("```\n")
		limit := totalRows
		if truncated {
			limit = maxRows
		}
		for i := 0; i < limit; i++ {
			sb.WriteString(strings.Join(rows[i], "\t"))
			sb.WriteString("\n")
		}
		if truncated {
			sb.WriteString(fmt.Sprintf("... [truncated, showing first %d of %d rows]\n", maxRows, totalRows))
		}
		sb.WriteString("```\n")
	}

	return sb.String(), nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./internal/slack/ -run TestParseXlsx -v
```

Expected: all 4 tests PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/slack/xlsx.go internal/slack/xlsx_test.go
git commit -m "feat: add xlsx parsing with per-sheet row truncation"
```

---

### Task 4: FetchedMessage struct and binary download

**Files:**
- Modify: `internal/slack/client.go`

- [ ] **Step 1: Add ImageData and FetchedMessage structs and downloadBytes**

In `internal/slack/client.go`, add after the `Client` struct:

```go
// ImageData holds a downloaded image for vision processing.
type ImageData struct {
	Name      string // original filename
	MimeType  string // "image/png", "image/jpeg"
	Data      []byte // raw image bytes
	Permalink string // Slack permalink for fallback/issue body
}

// FetchedMessage contains the text and extracted images from a Slack message.
type FetchedMessage struct {
	Text   string      // message text + inlined text/xlsx content
	Images []ImageData // jpg/png image bytes for vision
}
```

Add a `downloadBytes` method after the existing `downloadFile`:

```go
func (c *Client) downloadBytes(url string) ([]byte, error) {
	if url == "" {
		return nil, fmt.Errorf("empty download URL")
	}
	var buf bytes.Buffer
	err := c.api.GetFile(url, &buf)
	if err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
```

Add `isVisionImage` helper after `isImageFile`:

```go
// isVisionImage returns true for image types supported by vision APIs.
func isVisionImage(filetype string) bool {
	return filetype == "png" || filetype == "jpg" || filetype == "jpeg"
}
```

Add `"bytes"` to the imports.

- [ ] **Step 2: Verify build**

```bash
go build ./...
```

Expected: success.

- [ ] **Step 3: Commit**

```bash
git add internal/slack/client.go
git commit -m "feat: add FetchedMessage, ImageData structs and downloadBytes"
```

---

### Task 5: Refactor FetchMessage to return FetchedMessage

**Files:**
- Modify: `internal/slack/client.go`
- Modify: `internal/bot/workflow.go`

- [ ] **Step 1: Change FetchMessage signature and implementation**

Change `FetchMessage` to return `FetchedMessage`:

```go
const (
	maxImageSize  = 20 * 1024 * 1024 // 20 MB
	maxImageCount = 5
)

func (c *Client) FetchMessage(channelID, messageTS string) (FetchedMessage, error) {
	params := &slack.GetConversationHistoryParameters{
		ChannelID: channelID,
		Latest:    messageTS,
		Inclusive: true,
		Limit:     1,
	}
	history, err := c.api.GetConversationHistory(params)
	if err != nil {
		return FetchedMessage{}, fmt.Errorf("fetch message: %w", err)
	}
	if len(history.Messages) == 0 {
		return FetchedMessage{}, fmt.Errorf("message not found at ts=%s", messageTS)
	}

	msg := history.Messages[0]
	text := msg.Text
	var images []ImageData

	for _, f := range msg.Files {
		if isTextFile(f.Filetype, f.Mimetype) {
			content, dlErr := c.downloadFile(f.URLPrivateDownload)
			if dlErr != nil {
				slog.Warn("failed to download slack file", "name", f.Name, "error", dlErr)
				text += fmt.Sprintf("\n\n[附件: %s](%s)", f.Name, f.Permalink)
				continue
			}
			lines := strings.Split(content, "\n")
			if len(lines) > 500 {
				content = strings.Join(lines[:500], "\n") + "\n... [truncated]"
			}
			text += fmt.Sprintf("\n\n--- 附件: %s ---\n```\n%s\n```", f.Name, content)
		} else if f.Filetype == "xlsx" {
			data, dlErr := c.downloadBytes(f.URLPrivateDownload)
			if dlErr != nil {
				slog.Warn("failed to download xlsx", "name", f.Name, "error", dlErr)
				text += fmt.Sprintf("\n\n[附件: %s](%s)", f.Name, f.Permalink)
				continue
			}
			parsed, parseErr := parseXlsx(data, defaultMaxXlsxRows)
			if parseErr != nil {
				slog.Warn("failed to parse xlsx", "name", f.Name, "error", parseErr)
				text += fmt.Sprintf("\n\n[附件: %s](%s)", f.Name, f.Permalink)
				continue
			}
			text += fmt.Sprintf("\n\n--- 附件: %s ---\n%s", f.Name, parsed)
		} else if isVisionImage(f.Filetype) && len(images) < maxImageCount {
			data, dlErr := c.downloadBytes(f.URLPrivateDownload)
			if dlErr != nil {
				slog.Warn("failed to download image", "name", f.Name, "error", dlErr)
				text += fmt.Sprintf("\n\n[圖片: %s](%s)", f.Name, f.Permalink)
				continue
			}
			if len(data) > maxImageSize {
				slog.Warn("image too large, skipping", "name", f.Name, "size", len(data))
				text += fmt.Sprintf("\n\n[圖片: %s](%s)", f.Name, f.Permalink)
				continue
			}
			images = append(images, ImageData{
				Name:      f.Name,
				MimeType:  f.Mimetype,
				Data:      data,
				Permalink: f.Permalink,
			})
			// Keep text annotation for GitHub issue body
			text += fmt.Sprintf("\n\n[圖片: %s](%s)", f.Name, f.Permalink)
		} else if isImageFile(f.Filetype, f.Mimetype) {
			text += fmt.Sprintf("\n\n[圖片: %s](%s)", f.Name, f.Permalink)
		} else {
			text += fmt.Sprintf("\n\n[附件: %s](%s)", f.Name, f.Permalink)
		}
	}

	return FetchedMessage{Text: text, Images: images}, nil
}
```

- [ ] **Step 2: Update workflow.go callers**

In `internal/bot/workflow.go`, change `HandleReaction` (around line 117):

Replace:
```go
	message, err := w.slack.FetchMessage(event.ChannelID, event.MessageTS)
```

With:
```go
	fetched, err := w.slack.FetchMessage(event.ChannelID, event.MessageTS)
```

And update the `pendingIssue` struct to add `Images`:

```go
type pendingIssue struct {
	Event          slackclient.ReactionEvent
	ReactionCfg    config.ReactionConfig
	ChannelCfg     config.ChannelConfig
	Message        string
	Images         []slackclient.ImageData // new
	Reporter       string
	ChannelName    string
	ThreadTS       string
	SelectorTS     string
	SelectedRepo   string
	SelectedBranch string
	Phase          string
}
```

Update the `pi` initialization in `HandleReaction`:

Replace:
```go
	message = enrichMessage(message, w.mantisClient)
```
With:
```go
	fetched.Text = enrichMessage(fetched.Text, w.mantisClient)
```

Replace:
```go
	pi := &pendingIssue{
		Event:       event,
		ReactionCfg: reactionCfg,
		ChannelCfg:  channelCfg,
		Message:     message,
		Reporter:    reporter,
		ChannelName: channelName,
		ThreadTS:    event.MessageTS,
	}
```
With:
```go
	pi := &pendingIssue{
		Event:       event,
		ReactionCfg: reactionCfg,
		ChannelCfg:  channelCfg,
		Message:     fetched.Text,
		Images:      fetched.Images,
		Reporter:    reporter,
		ChannelName: channelName,
		ThreadTS:    event.MessageTS,
	}
```

- [ ] **Step 3: Verify build**

```bash
go build ./...
```

Expected: success. Some tests may not compile yet if they mock FetchMessage — fix any callers.

- [ ] **Step 4: Run all tests**

```bash
go test ./...
```

Expected: all tests pass.

- [ ] **Step 5: Commit**

```bash
git add internal/slack/client.go internal/bot/workflow.go
git commit -m "feat: FetchMessage returns FetchedMessage with images and xlsx"
```

---

### Task 6: Diagnosis layer — images in DiagnoseInput and LoopInput

**Files:**
- Modify: `internal/diagnosis/engine.go`
- Modify: `internal/diagnosis/loop.go`
- Modify: `internal/diagnosis/cache.go`

- [ ] **Step 1: Write the failing test for token estimation with images**

In `internal/diagnosis/loop_test.go`, add:

```go
func TestEstimateMessages_WithImages(t *testing.T) {
	msgs := []llm.Message{
		{Role: "user", Content: "hello", Images: []llm.ImageContent{
			{Name: "a.png", MimeType: "image/png", Data: make([]byte, 100)},
			{Name: "b.jpg", MimeType: "image/jpeg", Data: make([]byte, 100)},
		}},
	}
	withImages := estimateMessages(msgs)

	msgsNoImg := []llm.Message{
		{Role: "user", Content: "hello"},
	}
	withoutImages := estimateMessages(msgsNoImg)

	// Each image adds 1600 tokens.
	expected := withoutImages + 2*1600
	if withImages != expected {
		t.Errorf("expected %d tokens with images, got %d", expected, withImages)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./internal/diagnosis/ -run TestEstimateMessages_WithImages -v
```

Expected: FAIL — images not counted.

- [ ] **Step 3: Update estimateMessages to count images**

In `internal/diagnosis/loop.go`, change `estimateMessages`:

```go
const tokensPerImage = 1600

func estimateMessages(msgs []llm.Message) int {
	total := 0
	for _, m := range msgs {
		total += estimateTokens(m.Content)
		total += len(m.Images) * tokensPerImage
	}
	return total
}
```

- [ ] **Step 4: Run test to verify it passes**

```bash
go test ./internal/diagnosis/ -run TestEstimateMessages_WithImages -v
```

Expected: PASS.

- [ ] **Step 5: Add Images to DiagnoseInput and LoopInput**

In `internal/diagnosis/engine.go`, add `Images` to `DiagnoseInput`:

```go
type DiagnoseInput struct {
	Type     string
	Message  string
	Images   []llm.ImageContent
	RepoPath string
	Keywords []string
	Prompt   llm.PromptOptions
}
```

In `internal/diagnosis/loop.go`, add `Images` to `LoopInput`:

```go
type LoopInput struct {
	Type      string
	Message   string
	Images    []llm.ImageContent
	RepoPath  string
	Keywords  []string
	Prompt    llm.PromptOptions
	MaxTurns  int
	MaxTokens int
}
```

- [ ] **Step 6: Pass images to first user message in RunLoop**

In `internal/diagnosis/loop.go`, change the initial messages construction (around line 74):

```go
	messages := []llm.Message{
		{Role: "user", Content: fmt.Sprintf("## %s Report\n\nRepository: %s\n\n> %s%s", typeLabel, input.RepoPath, input.Message, preGrepSection), Images: input.Images},
	}
```

- [ ] **Step 7: Pass images from Engine.Diagnose to RunLoop**

In `internal/diagnosis/engine.go`, update the `Diagnose` method's `RunLoop` call:

```go
	resp, err := RunLoop(ctx, e.chain, e.tools, LoopInput{
		Type:      input.Type,
		Message:   input.Message,
		Images:    input.Images,
		RepoPath:  input.RepoPath,
		Keywords:  input.Keywords,
		Prompt:    input.Prompt,
		MaxTurns:  e.maxTurns,
		MaxTokens: e.maxTokens,
	})
```

- [ ] **Step 8: Update cache key to include image count**

In `internal/diagnosis/cache.go`, change the `Key` method signature and implementation:

```go
func (c *Cache) Key(repo, branch, message, language string, extraRules []string, imageCount int) string {
	sorted := make([]string, len(extraRules))
	copy(sorted, extraRules)
	sort.Strings(sorted)

	raw := strings.Join([]string{
		repo,
		branch,
		message,
		language,
		strings.Join(sorted, "|"),
		fmt.Sprintf("images:%d", imageCount),
	}, "\x00")

	h := sha256.Sum256([]byte(raw))
	return fmt.Sprintf("%x", h)
}
```

Update the caller in `engine.go`:

```go
	cacheKey := e.cache.Key(input.RepoPath, "", input.Message,
		input.Prompt.Language, input.Prompt.ExtraRules, len(input.Images))
```

- [ ] **Step 9: Run all tests**

```bash
go test ./...
```

Expected: all tests pass. Some cache tests may need the new `imageCount` parameter — add `0` as the last arg to existing `Key` calls in tests.

- [ ] **Step 10: Commit**

```bash
git add internal/diagnosis/engine.go internal/diagnosis/loop.go internal/diagnosis/loop_test.go internal/diagnosis/cache.go internal/diagnosis/cache_test.go
git commit -m "feat: pass images through diagnosis pipeline with token estimation"
```

---

### Task 7: System prompt update for image context

**Files:**
- Modify: `internal/llm/prompt.go`

- [ ] **Step 1: Add hasImages parameter to AgentSystemPrompt**

In `internal/llm/prompt.go`, change the signature and add conditional image guidance:

```go
func AgentSystemPrompt(diagType string, opts PromptOptions, hasImages bool) string {
```

After the existing search strategy section (before `sb.WriteString(agentOutputSchema(diagType))`), add:

```go
	if hasImages {
		sb.WriteString(`If screenshots or images are attached, use them to understand the reported behavior, error messages, or UI state. Reference what you see in the images when relevant to your triage.

`)
	}
```

- [ ] **Step 2: Update callers**

In `internal/diagnosis/loop.go`, update the `baseSystem` line:

```go
	baseSystem := llm.AgentSystemPrompt(input.Type, input.Prompt, len(input.Images) > 0)
```

- [ ] **Step 3: Run all tests**

```bash
go test ./...
```

Expected: all tests pass. Any existing calls to `AgentSystemPrompt` in tests need the third `false` arg.

- [ ] **Step 4: Commit**

```bash
git add internal/llm/prompt.go internal/diagnosis/loop.go
git commit -m "feat: add conditional image guidance to agent system prompt"
```

---

### Task 8: Claude provider vision support (TDD)

**Files:**
- Modify: `internal/llm/claude.go`
- Create: `internal/llm/claude_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/llm/claude_test.go`:

```go
package llm

import (
	"encoding/json"
	"testing"
)

func TestClaudeUserMessage_WithImages(t *testing.T) {
	msg := Message{
		Role:    "user",
		Content: "check this bug",
		Images: []ImageContent{
			{Name: "err.png", MimeType: "image/png", Data: []byte("fakepng")},
		},
	}

	blocks := buildClaudeUserContent(msg)

	data, err := json.Marshal(blocks)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	var arr []map[string]any
	if err := json.Unmarshal(data, &arr); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if len(arr) != 2 {
		t.Fatalf("expected 2 content blocks, got %d", len(arr))
	}

	// First block: image
	if arr[0]["type"] != "image" {
		t.Errorf("expected first block type=image, got %v", arr[0]["type"])
	}
	source := arr[0]["source"].(map[string]any)
	if source["type"] != "base64" {
		t.Errorf("expected source type=base64, got %v", source["type"])
	}
	if source["media_type"] != "image/png" {
		t.Errorf("expected media_type=image/png, got %v", source["media_type"])
	}

	// Second block: text
	if arr[1]["type"] != "text" {
		t.Errorf("expected second block type=text, got %v", arr[1]["type"])
	}
	if arr[1]["text"] != "check this bug" {
		t.Errorf("expected text content, got %v", arr[1]["text"])
	}
}

func TestClaudeUserMessage_WithoutImages(t *testing.T) {
	msg := Message{
		Role:    "user",
		Content: "just text",
	}

	result := buildClaudeUserContent(msg)

	// Without images, should return plain string.
	str, ok := result.(string)
	if !ok {
		t.Fatalf("expected string content, got %T", result)
	}
	if str != "just text" {
		t.Errorf("expected 'just text', got %q", str)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./internal/llm/ -run TestClaudeUserMessage -v
```

Expected: FAIL — `buildClaudeUserContent` not defined.

- [ ] **Step 3: Implement buildClaudeUserContent**

In `internal/llm/claude.go`, add the helper function and update the Chat method.

Add `"encoding/base64"` to imports.

Add helper:

```go
// buildClaudeUserContent returns content for a user message.
// Without images: returns the plain string.
// With images: returns a content blocks array (images first, then text).
func buildClaudeUserContent(m Message) any {
	if len(m.Images) == 0 {
		return m.Content
	}

	var blocks []map[string]any
	for _, img := range m.Images {
		blocks = append(blocks, map[string]any{
			"type": "image",
			"source": map[string]any{
				"type":       "base64",
				"media_type": img.MimeType,
				"data":       base64.StdEncoding.EncodeToString(img.Data),
			},
		})
	}
	blocks = append(blocks, map[string]any{
		"type": "text",
		"text": m.Content,
	})
	return blocks
}
```

In the `Chat` method, update the `case "user":` block:

```go
		case "user":
			msgs = append(msgs, map[string]any{
				"role":    "user",
				"content": buildClaudeUserContent(m),
			})
```

- [ ] **Step 4: Run tests**

```bash
go test ./internal/llm/ -run TestClaudeUserMessage -v
```

Expected: PASS.

- [ ] **Step 5: Run all tests**

```bash
go test ./...
```

Expected: all tests pass.

- [ ] **Step 6: Commit**

```bash
git add internal/llm/claude.go internal/llm/claude_test.go
git commit -m "feat: Claude provider vision support with content blocks"
```

---

### Task 9: OpenAI provider vision support (TDD)

**Files:**
- Modify: `internal/llm/openai.go`
- Create: `internal/llm/openai_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/llm/openai_test.go`:

```go
package llm

import (
	"encoding/json"
	"testing"
)

func TestOpenAIUserContent_WithImages(t *testing.T) {
	msg := Message{
		Role:    "user",
		Content: "check this",
		Images: []ImageContent{
			{Name: "shot.png", MimeType: "image/png", Data: []byte("fakepng")},
		},
	}

	blocks := buildOpenAIUserContent(msg)

	data, err := json.Marshal(blocks)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	var arr []map[string]any
	if err := json.Unmarshal(data, &arr); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if len(arr) != 2 {
		t.Fatalf("expected 2 content blocks, got %d", len(arr))
	}

	// First block: image_url
	if arr[0]["type"] != "image_url" {
		t.Errorf("expected type=image_url, got %v", arr[0]["type"])
	}
	imgURL := arr[0]["image_url"].(map[string]any)
	urlStr := imgURL["url"].(string)
	if len(urlStr) == 0 {
		t.Error("expected non-empty data URI")
	}
	if urlStr[:len("data:image/png;base64,")] != "data:image/png;base64," {
		t.Errorf("expected data URI prefix, got %q", urlStr[:30])
	}

	// Second block: text
	if arr[1]["type"] != "text" {
		t.Errorf("expected type=text, got %v", arr[1]["type"])
	}
}

func TestOpenAIUserContent_WithoutImages(t *testing.T) {
	msg := Message{
		Role:    "user",
		Content: "plain text",
	}

	result := buildOpenAIUserContent(msg)

	str, ok := result.(string)
	if !ok {
		t.Fatalf("expected string, got %T", result)
	}
	if str != "plain text" {
		t.Errorf("expected 'plain text', got %q", str)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./internal/llm/ -run TestOpenAIUserContent -v
```

Expected: FAIL — `buildOpenAIUserContent` not defined.

- [ ] **Step 3: Implement buildOpenAIUserContent**

In `internal/llm/openai.go`, add `"encoding/base64"` to imports. Add helper:

```go
// buildOpenAIUserContent returns content for a user message.
// Without images: returns plain string.
// With images: returns content blocks array with image_url and text.
func buildOpenAIUserContent(m Message) any {
	if len(m.Images) == 0 {
		return m.Content
	}

	var blocks []map[string]any
	for _, img := range m.Images {
		dataURI := fmt.Sprintf("data:%s;base64,%s", img.MimeType, base64.StdEncoding.EncodeToString(img.Data))
		blocks = append(blocks, map[string]any{
			"type": "image_url",
			"image_url": map[string]string{
				"url": dataURI,
			},
		})
	}
	blocks = append(blocks, map[string]any{
		"type": "text",
		"text": m.Content,
	})
	return blocks
}
```

In the `Chat` method, update `case "user":`:

```go
		case "user":
			msgs = append(msgs, map[string]any{
				"role":    "user",
				"content": buildOpenAIUserContent(m),
			})
```

- [ ] **Step 4: Run tests**

```bash
go test ./internal/llm/ -run TestOpenAIUserContent -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/llm/openai.go internal/llm/openai_test.go
git commit -m "feat: OpenAI provider vision support with image_url content blocks"
```

---

### Task 10: CLI provider vision support (TDD)

**Files:**
- Modify: `internal/llm/cli.go`
- Modify: `internal/llm/cli_test.go`

- [ ] **Step 1: Write the failing tests**

In `internal/llm/cli_test.go`, add:

```go
import (
	"os"
	"strings"
)

func TestCLIProvider_BuildImageArgs_Claude(t *testing.T) {
	p := NewCLIProvider("test", "claude", []string{"--print", "{prompt}"}, 10*time.Second)

	images := []ImageContent{
		{Name: "a.png", MimeType: "image/png", Data: []byte("fakepng")},
		{Name: "b.jpg", MimeType: "image/jpeg", Data: []byte("fakejpg")},
	}

	tmpFiles, fileArgs := p.prepareImageFiles(images)
	defer func() {
		for _, f := range tmpFiles {
			os.Remove(f)
		}
	}()

	if len(tmpFiles) != 2 {
		t.Fatalf("expected 2 temp files, got %d", len(tmpFiles))
	}
	if len(fileArgs) != 4 {
		t.Fatalf("expected 4 file args (--file x2), got %d: %v", len(fileArgs), fileArgs)
	}

	// Verify temp files exist and contain data.
	for _, f := range tmpFiles {
		info, err := os.Stat(f)
		if err != nil {
			t.Errorf("temp file %s not found: %v", f, err)
		}
		if info.Size() == 0 {
			t.Errorf("temp file %s is empty", f)
		}
	}

	// Verify --file flags.
	if fileArgs[0] != "--file" || fileArgs[2] != "--file" {
		t.Errorf("expected --file flags, got %v", fileArgs)
	}
}

func TestCLIProvider_BuildImageArgs_NonClaude(t *testing.T) {
	p := NewCLIProvider("test", "some-other-tool", []string{"{prompt}"}, 10*time.Second)

	images := []ImageContent{
		{Name: "a.png", MimeType: "image/png", Data: []byte("fakepng")},
	}

	tmpFiles, fileArgs := p.prepareImageFiles(images)
	if len(tmpFiles) != 0 {
		t.Errorf("non-claude tool should not create temp files, got %d", len(tmpFiles))
	}
	if len(fileArgs) != 0 {
		t.Errorf("non-claude tool should not produce file args, got %v", fileArgs)
	}
}

func TestCLIProvider_ImageFallbackText(t *testing.T) {
	images := []ImageContent{
		{Name: "screen.png", MimeType: "image/png", Data: []byte("data")},
	}
	text := imageFallbackText(images)
	if !strings.Contains(text, "[圖片: screen.png]") {
		t.Errorf("expected fallback text, got %q", text)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./internal/llm/ -run "TestCLIProvider_BuildImageArgs|TestCLIProvider_ImageFallback" -v
```

Expected: FAIL — `prepareImageFiles` and `imageFallbackText` not defined.

- [ ] **Step 3: Implement prepareImageFiles and imageFallbackText**

In `internal/llm/cli.go`, add `"os"` and `"path/filepath"` to imports. Add:

```go
// prepareImageFiles writes images to temp files and returns paths + CLI args.
// Only creates files for claude CLI. Non-claude tools return empty (use text fallback).
func (c *CLIProvider) prepareImageFiles(images []ImageContent) (tmpFiles []string, fileArgs []string) {
	if len(images) == 0 {
		return nil, nil
	}
	if !strings.Contains(c.command, "claude") {
		return nil, nil
	}

	for _, img := range images {
		ext := ".png"
		if strings.Contains(img.MimeType, "jpeg") || strings.Contains(img.MimeType, "jpg") {
			ext = ".jpg"
		}

		tmp, err := os.CreateTemp("", "slack-issue-bot-*"+ext)
		if err != nil {
			slog.Warn("failed to create temp image file", "name", img.Name, "error", err)
			continue
		}
		if _, err := tmp.Write(img.Data); err != nil {
			slog.Warn("failed to write temp image file", "name", img.Name, "error", err)
			tmp.Close()
			os.Remove(tmp.Name())
			continue
		}
		tmp.Close()

		tmpFiles = append(tmpFiles, tmp.Name())
		fileArgs = append(fileArgs, "--file", tmp.Name())
	}
	return tmpFiles, fileArgs
}

// imageFallbackText returns text annotations for images (used when vision is not supported).
func imageFallbackText(images []ImageContent) string {
	var sb strings.Builder
	for _, img := range images {
		sb.WriteString(fmt.Sprintf("\n[圖片: %s]", img.Name))
	}
	return sb.String()
}
```

- [ ] **Step 4: Integrate into Chat method**

In the `Chat` method, after the prompt is built but before the command is executed, add image handling. Replace the section from `fullPrompt := sb.String()` through `args, useStdin := c.buildArgs(fullPrompt)`:

```go
	fullPrompt := sb.String()

	// Handle images: claude CLI gets --file flags, others get text fallback.
	var allImages []ImageContent
	for _, m := range req.Messages {
		allImages = append(allImages, m.Images...)
	}

	tmpFiles, fileArgs := c.prepareImageFiles(allImages)
	defer func() {
		for _, f := range tmpFiles {
			os.Remove(f)
		}
	}()

	// Non-claude CLI or failed temp files: append text fallback to prompt.
	if len(allImages) > 0 && len(tmpFiles) == 0 {
		fullPrompt += imageFallbackText(allImages)
	}

	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	args, useStdin := c.buildArgs(fullPrompt)
	// Prepend --file args before other args.
	if len(fileArgs) > 0 {
		args = append(fileArgs, args...)
	}
```

- [ ] **Step 5: Run tests**

```bash
go test ./internal/llm/ -run "TestCLIProvider_BuildImageArgs|TestCLIProvider_ImageFallback" -v
```

Expected: PASS.

- [ ] **Step 6: Run all tests**

```bash
go test ./...
```

Expected: all tests pass.

- [ ] **Step 7: Commit**

```bash
git add internal/llm/cli.go internal/llm/cli_test.go
git commit -m "feat: CLI provider vision support with temp files and --file flags"
```

---

### Task 11: Ollama provider image fallback

**Files:**
- Modify: `internal/llm/ollama.go`

- [ ] **Step 1: Update Ollama user message handling**

In `internal/llm/ollama.go`, update the `case "user":` block inside the `Chat` method:

```go
		case "user":
			content := m.Content
			if len(m.Images) > 0 {
				content += imageFallbackText(m.Images)
			}
			msgs = append(msgs, map[string]string{
				"role":    "user",
				"content": content,
			})
```

Note: `imageFallbackText` is defined in `cli.go` and is in the same package, so it's accessible.

- [ ] **Step 2: Run all tests**

```bash
go test ./...
```

Expected: all tests pass.

- [ ] **Step 3: Commit**

```bash
git add internal/llm/ollama.go
git commit -m "feat: Ollama provider image text fallback"
```

---

### Task 12: Workflow plumbing — pass images to diagnosis

**Files:**
- Modify: `internal/bot/workflow.go`

- [ ] **Step 1: Convert ImageData to ImageContent and pass to DiagnoseInput**

In the `createIssue` method of `internal/bot/workflow.go`, update the `diagInput` construction. Add a conversion helper at the bottom of the file:

```go
func toImageContent(images []slackclient.ImageData) []llm.ImageContent {
	result := make([]llm.ImageContent, len(images))
	for i, img := range images {
		result[i] = llm.ImageContent{
			Name:     img.Name,
			MimeType: img.MimeType,
			Data:     img.Data,
		}
	}
	return result
}
```

Update the `diagInput` construction in `createIssue`:

```go
	diagInput := diagnosis.DiagnoseInput{
		Type:     pi.ReactionCfg.Type,
		Message:  pi.Message,
		Images:   toImageContent(pi.Images),
		RepoPath: repoPath,
		Keywords: keywords,
		Prompt: llm.PromptOptions{
			Language:   w.cfg.Diagnosis.Prompt.Language,
			ExtraRules: w.cfg.Diagnosis.Prompt.ExtraRules,
		},
	}
```

- [ ] **Step 2: Verify build and run all tests**

```bash
go build ./... && go test ./...
```

Expected: all tests pass.

- [ ] **Step 3: Commit**

```bash
git add internal/bot/workflow.go
git commit -m "feat: wire images from pendingIssue through to diagnosis engine"
```

---

### Task 13: Integration test — images flow end-to-end

**Files:**
- Modify: `internal/diagnosis/loop_test.go`

- [ ] **Step 1: Write integration test**

In `internal/diagnosis/loop_test.go`, add:

```go
func TestRunLoop_ImagesOnlyInFirstMessage(t *testing.T) {
	grepArgs, _ := json.Marshal(map[string]string{"pattern": "Login"})

	// Track what the mock receives.
	var receivedRequests []llm.ChatRequest
	mock := &mockConvProvider{
		responses: []llm.ChatResponse{
			// Turn 1: tool call.
			{
				ToolCalls:  []llm.ToolCall{{ID: "tc_1", Name: "grep", Args: grepArgs}},
				StopReason: llm.StopReasonToolUse,
			},
			// Turn 2: triage card.
			{
				Content:    triageJSON("found it", "high"),
				StopReason: llm.StopReasonFinish,
			},
		},
	}

	// Wrap mock to capture requests.
	capturing := &capturingProvider{inner: mock, requests: &receivedRequests}

	images := []llm.ImageContent{
		{Name: "err.png", MimeType: "image/png", Data: []byte("fakepng")},
	}

	_, err := RunLoop(context.Background(), capturing, AllTools(), LoopInput{
		Type:     "bug",
		Message:  "login crashes",
		Images:   images,
		RepoPath: t.TempDir(),
		MaxTurns: 5,
	})
	if err != nil {
		t.Fatalf("RunLoop failed: %v", err)
	}

	if len(receivedRequests) != 2 {
		t.Fatalf("expected 2 Chat calls, got %d", len(receivedRequests))
	}

	// First call: first user message should have images.
	firstMsgs := receivedRequests[0].Messages
	hasImages := false
	for _, m := range firstMsgs {
		if m.Role == "user" && len(m.Images) > 0 {
			hasImages = true
			break
		}
	}
	if !hasImages {
		t.Error("expected first Chat call to have images in user message")
	}

	// Second call: subsequent messages should NOT have images.
	secondMsgs := receivedRequests[1].Messages
	for _, m := range secondMsgs {
		if m.Role != "user" {
			continue
		}
		// Only the first user message (index 0) should have images.
		// Messages added after turn 1 (tool_result, new user) should not.
	}
	// Count user messages with images in second request.
	imgCount := 0
	for _, m := range secondMsgs {
		if len(m.Images) > 0 {
			imgCount++
		}
	}
	// Only the original first user message should have images.
	if imgCount != 1 {
		t.Errorf("expected exactly 1 message with images across all turns, got %d", imgCount)
	}
}
```

Add the capturing provider helper:

```go
type capturingProvider struct {
	inner    llm.ConversationProvider
	requests *[]llm.ChatRequest
}

func (c *capturingProvider) Name() string { return c.inner.Name() }
func (c *capturingProvider) Chat(ctx context.Context, req llm.ChatRequest) (llm.ChatResponse, error) {
	*c.requests = append(*c.requests, req)
	return c.inner.Chat(ctx, req)
}
```

- [ ] **Step 2: Run the integration test**

```bash
go test ./internal/diagnosis/ -run TestRunLoop_ImagesOnlyInFirstMessage -v
```

Expected: PASS.

- [ ] **Step 3: Run full test suite**

```bash
go test ./...
```

Expected: all tests pass.

- [ ] **Step 4: Commit**

```bash
git add internal/diagnosis/loop_test.go
git commit -m "test: integration test for images flowing through agent loop"
```

---

## Self-Review Checklist

1. **Spec coverage**: xlsx parsing (Task 3), FetchedMessage (Task 4-5), token estimation (Task 6), cache key (Task 6), DiagnoseInput/LoopInput (Task 6), system prompt (Task 7), Claude (Task 8), OpenAI (Task 9), CLI (Task 10), Ollama (Task 11), workflow plumbing (Task 12), integration test (Task 13). Image count limit and size limit handled in Task 5. All spec items covered.

2. **Placeholder scan**: no TBD/TODO/fill-in-later found. All code blocks complete.

3. **Type consistency**: `ImageContent` (provider.go) used consistently in diagnosis and LLM layers. `ImageData` (client.go) used in Slack layer. `toImageContent` converts between them in workflow.go. `buildClaudeUserContent`, `buildOpenAIUserContent`, `prepareImageFiles`, `imageFallbackText` names consistent across tasks. `parseXlsx` signature consistent between Task 3 (implementation) and Task 5 (caller).
