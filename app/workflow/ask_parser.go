package workflow

import (
	"encoding/json"
	"fmt"
	"strings"
)

const askMarker = "===ASK_RESULT==="

// AskResult is the parsed ===ASK_RESULT=== JSON.
type AskResult struct {
	Answer     string `json:"answer"`
	Confidence string `json:"confidence,omitempty"`
}

// ParseAskOutput extracts the ASK_RESULT marker block and unmarshals its
// JSON body. Walks markers last-first so the opencode fence pattern
// (payload sandwiched between opening and closing markers) is recovered.
// Rejects empty answers.
func ParseAskOutput(output string) (AskResult, error) {
	output = strings.TrimSpace(output)
	segments := segmentAfterMarker(output, askMarker)
	if len(segments) == 0 {
		return AskResult{}, fmt.Errorf("%s marker not found", askMarker)
	}
	var lastErr error
	for _, seg := range segments {
		if !strings.HasPrefix(seg, "{") {
			lastErr = fmt.Errorf("expected JSON object after marker")
			continue
		}
		jsonStr := extractJSON(seg)
		var r AskResult
		if err := json.Unmarshal([]byte(jsonStr), &r); err != nil {
			lastErr = fmt.Errorf("unmarshal: %w", err)
			continue
		}
		if strings.TrimSpace(r.Answer) == "" {
			lastErr = fmt.Errorf("answer must not be empty")
			continue
		}
		return r, nil
	}
	return AskResult{}, lastErr
}
