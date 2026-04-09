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
	str, ok := result.(string)
	if !ok {
		t.Fatalf("expected string content, got %T", result)
	}
	if str != "just text" {
		t.Errorf("expected 'just text', got %q", str)
	}
}
