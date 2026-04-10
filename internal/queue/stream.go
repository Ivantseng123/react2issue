package queue

import (
	"bufio"
	"encoding/json"
	"io"
	"strings"
)

type StreamEvent struct {
	Type         string
	ToolName     string
	TextBytes    int
	CostUSD      float64
	InputTokens  int
	OutputTokens int
}

// ReadStreamJSON reads NDJSON from claude --output-format stream-json.
// Returns final text from "result" event, or reassembled message_delta as fallback.
func ReadStreamJSON(r io.Reader, eventCh chan<- StreamEvent) string {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

	var reassembled strings.Builder
	var resultText string

	for scanner.Scan() {
		line := scanner.Text()
		var raw map[string]any
		if json.Unmarshal([]byte(line), &raw) != nil {
			continue
		}

		eventType, _ := raw["type"].(string)
		switch eventType {
		case "message_delta":
			if delta, ok := raw["delta"].(map[string]any); ok {
				if text, ok := delta["text"].(string); ok {
					reassembled.WriteString(text)
					select {
					case eventCh <- StreamEvent{Type: "message_delta", TextBytes: len(text)}:
					default:
					}
				}
			}
		case "tool_use":
			name, _ := raw["name"].(string)
			select {
			case eventCh <- StreamEvent{Type: "tool_use", ToolName: name}:
			default:
			}
		case "result":
			if res, ok := raw["result"].(string); ok {
				resultText = res
			}
			costEvent := StreamEvent{Type: "result"}
			if cost, ok := raw["total_cost_usd"].(float64); ok {
				costEvent.CostUSD = cost
			}
			if usage, ok := raw["usage"].(map[string]any); ok {
				if in, ok := usage["input_tokens"].(float64); ok {
					costEvent.InputTokens = int(in)
				}
				if out, ok := usage["output_tokens"].(float64); ok {
					costEvent.OutputTokens = int(out)
				}
			}
			select {
			case eventCh <- costEvent:
			default:
			}
		}
	}

	if resultText != "" {
		return resultText
	}
	return reassembled.String()
}

// ReadRawOutput reads plain text stdout (non-stream agents).
func ReadRawOutput(r io.Reader) string {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	var buf strings.Builder
	for scanner.Scan() {
		buf.WriteString(scanner.Text())
		buf.WriteByte('\n')
	}
	return buf.String()
}
