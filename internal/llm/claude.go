package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type ClaudeProvider struct {
	apiKey  string
	model   string
	baseURL string
	client  *http.Client
}

func NewClaudeProvider(apiKey, model, baseURL string, timeout time.Duration) *ClaudeProvider {
	return &ClaudeProvider{
		apiKey:  apiKey,
		model:   model,
		baseURL: baseURL,
		client:  &http.Client{Timeout: timeout},
	}
}

func (c *ClaudeProvider) Name() string { return "claude" }

func (c *ClaudeProvider) Chat(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	// Build messages array for Claude Messages API.
	var msgs []map[string]any
	for _, m := range req.Messages {
		switch m.Role {
		case "user":
			msgs = append(msgs, map[string]any{
				"role":    "user",
				"content": m.Content,
			})
		case "assistant":
			// Build content blocks: text + tool_use
			var blocks []map[string]any
			if m.Content != "" {
				blocks = append(blocks, map[string]any{
					"type": "text",
					"text": m.Content,
				})
			}
			for _, tc := range m.ToolCalls {
				var input any
				if err := json.Unmarshal(tc.Args, &input); err != nil {
					input = string(tc.Args)
				}
				blocks = append(blocks, map[string]any{
					"type":  "tool_use",
					"id":    tc.ID,
					"name":  tc.Name,
					"input": input,
				})
			}
			msgs = append(msgs, map[string]any{
				"role":    "assistant",
				"content": blocks,
			})
		case "tool_result":
			msgs = append(msgs, map[string]any{
				"role": "user",
				"content": []map[string]any{
					{
						"type":        "tool_result",
						"tool_use_id": m.ToolCallID,
						"content":     m.Content,
					},
				},
			})
		}
	}

	// Build tools array.
	var tools []map[string]any
	for _, t := range req.Tools {
		tools = append(tools, map[string]any{
			"name":         t.Name,
			"description":  t.Description,
			"input_schema": t.InputSchema,
		})
	}

	body := map[string]any{
		"model":      c.model,
		"max_tokens": 4096,
		"messages":   msgs,
	}
	if req.SystemPrompt != "" {
		body["system"] = req.SystemPrompt
	}
	if len(tools) > 0 {
		body["tools"] = tools
	}

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return ChatResponse{}, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/v1/messages", bytes.NewReader(jsonBody))
	if err != nil {
		return ChatResponse{}, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", c.apiKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")

	resp, err := c.client.Do(httpReq)
	if err != nil {
		return ChatResponse{}, fmt.Errorf("call claude: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return ChatResponse{}, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return ChatResponse{}, fmt.Errorf("claude returned %d: %s", resp.StatusCode, string(respBody))
	}

	// Parse response: extract text and tool_use blocks from content array.
	var raw struct {
		Content []struct {
			Type  string          `json:"type"`
			Text  string          `json:"text"`
			ID    string          `json:"id"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
		StopReason string `json:"stop_reason"`
	}
	if err := json.Unmarshal(respBody, &raw); err != nil {
		return ChatResponse{}, fmt.Errorf("parse response: %w", err)
	}

	var result ChatResponse
	var textParts []string
	for _, block := range raw.Content {
		switch block.Type {
		case "text":
			textParts = append(textParts, block.Text)
		case "tool_use":
			result.ToolCalls = append(result.ToolCalls, ToolCall{
				ID:   block.ID,
				Name: block.Name,
				Args: block.Input,
			})
		}
	}
	result.Content = strings.Join(textParts, "")

	if raw.StopReason == "tool_use" {
		result.StopReason = StopReasonToolUse
	} else {
		result.StopReason = StopReasonFinish
	}

	return result, nil
}

// ParseLLMTextResponse extracts structured diagnosis from LLM text output.
func ParseLLMTextResponse(text string) (DiagnoseResponse, error) {
	var structured struct {
		Summary    string `json:"summary"`
		Files      []struct {
			Path        string `json:"path"`
			LineNumber  int    `json:"line_number"`
			Description string `json:"description"`
		} `json:"files"`
		Suggestions   []string `json:"suggestions"`
		Complexity    string   `json:"complexity"`
		OpenQuestions []string `json:"open_questions"`
		Confidence    string   `json:"confidence"`
	}

	jsonStr := text
	if idx := strings.Index(text, "```json"); idx != -1 {
		start := idx + 7
		end := strings.Index(text[start:], "```")
		if end != -1 {
			jsonStr = text[start : start+end]
		}
	} else if idx := strings.Index(text, "{"); idx != -1 {
		jsonStr = text[idx:]
	}

	if err := json.Unmarshal([]byte(strings.TrimSpace(jsonStr)), &structured); err != nil {
		return DiagnoseResponse{Summary: text}, nil
	}

	var files []FileRef
	for _, f := range structured.Files {
		files = append(files, FileRef{
			Path:        f.Path,
			LineNumber:  f.LineNumber,
			Description: f.Description,
		})
	}

	return DiagnoseResponse{
		Summary:       structured.Summary,
		Files:         files,
		Suggestions:   structured.Suggestions,
		Complexity:    structured.Complexity,
		OpenQuestions:  structured.OpenQuestions,
		Confidence:    structured.Confidence,
	}, nil
}
