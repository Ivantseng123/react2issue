package queue

import (
	"strings"
	"testing"
)

func TestReadStreamJSON_ResultEvent(t *testing.T) {
	input := `{"type":"assistant","message":{"content":[{"type":"text","text":"Looking at code..."}]}}
{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"/src/main.go"}}]}}
{"type":"assistant","message":{"content":[{"type":"text","text":"Found the issue..."}]}}
{"type":"result","result":"Final answer text here","total_cost_usd":0.042,"usage":{"input_tokens":8500,"output_tokens":1200}}`

	r := strings.NewReader(input)
	eventCh := make(chan StreamEvent, 100)
	text := ReadStreamJSON(r, eventCh)
	close(eventCh)

	if text != "Final answer text here" {
		t.Errorf("text = %q, want 'Final answer text here'", text)
	}

	var events []StreamEvent
	for e := range eventCh {
		events = append(events, e)
	}
	if len(events) < 3 {
		t.Fatalf("expected at least 3 events, got %d", len(events))
	}

	found := false
	for _, e := range events {
		if e.Type == "tool_use" && e.ToolName == "Read" {
			found = true
		}
	}
	if !found {
		t.Error("missing tool_use:Read event")
	}

	var resultEvent StreamEvent
	for _, e := range events {
		if e.Type == "result" {
			resultEvent = e
		}
	}
	if resultEvent.CostUSD != 0.042 {
		t.Errorf("cost = %f, want 0.042", resultEvent.CostUSD)
	}
	if resultEvent.InputTokens != 8500 {
		t.Errorf("input_tokens = %d, want 8500", resultEvent.InputTokens)
	}
}

func TestReadStreamJSON_FallbackToReassembly(t *testing.T) {
	input := `{"type":"assistant","message":{"content":[{"type":"text","text":"Hello "}]}}
{"type":"assistant","message":{"content":[{"type":"text","text":"World"}]}}`

	r := strings.NewReader(input)
	eventCh := make(chan StreamEvent, 100)
	text := ReadStreamJSON(r, eventCh)
	close(eventCh)

	if text != "Hello World" {
		t.Errorf("reassembled text = %q, want 'Hello World'", text)
	}
}

func TestReadOpenCodeJSON_AccumulatesTextParts(t *testing.T) {
	// opencode `run --format json` emits one NDJSON line per event.
	// Shape: {"type":"<event>","timestamp":...,"sessionID":"...", ...}
	// Text parts arrive via {"type":"text","part":{"type":"text","text":"..."}}
	input := `{"type":"step_start","timestamp":1,"sessionID":"s1","part":{}}
{"type":"tool_use","timestamp":2,"sessionID":"s1","part":{"type":"tool","tool":"bash","state":{"status":"completed"}}}
{"type":"text","timestamp":3,"sessionID":"s1","part":{"type":"text","text":"First paragraph."}}
{"type":"tool_use","timestamp":4,"sessionID":"s1","part":{"type":"tool","tool":"read","state":{"status":"completed"}}}
{"type":"text","timestamp":5,"sessionID":"s1","part":{"type":"text","text":"Second paragraph."}}
{"type":"step_finish","timestamp":6,"sessionID":"s1","part":{}}`

	r := strings.NewReader(input)
	eventCh := make(chan StreamEvent, 100)
	text := ReadOpenCodeJSON(r, eventCh)
	close(eventCh)

	if !strings.Contains(text, "First paragraph.") || !strings.Contains(text, "Second paragraph.") {
		t.Errorf("text missing paragraphs: %q", text)
	}
	if strings.Contains(text, "bash") || strings.Contains(text, "tool_use") {
		t.Errorf("text should not contain tool noise: %q", text)
	}

	var tools []string
	for e := range eventCh {
		if e.Type == "tool_use" {
			tools = append(tools, e.ToolName)
		}
	}
	if len(tools) != 2 || tools[0] != "bash" || tools[1] != "read" {
		t.Errorf("expected [bash, read] tool_use events, got %v", tools)
	}
}

func TestReadOpenCodeJSON_IgnoresMalformedLines(t *testing.T) {
	input := `not json at all
{"type":"text","part":{"type":"text","text":"kept"}}
{broken json
`
	r := strings.NewReader(input)
	eventCh := make(chan StreamEvent, 10)
	text := ReadOpenCodeJSON(r, eventCh)
	close(eventCh)

	if !strings.Contains(text, "kept") {
		t.Errorf("should keep valid text event: %q", text)
	}
}

func TestReadRawOutput(t *testing.T) {
	input := "line1\nline2\nline3"
	r := strings.NewReader(input)
	text := ReadRawOutput(r)
	if !strings.Contains(text, "line1") || !strings.Contains(text, "line3") {
		t.Errorf("raw output = %q", text)
	}
}
