package workflow

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

const askMarker = "===ASK_RESULT==="

// ResultSource flags transport-quality of an AskResult. Spec §AskResult
// Metadata explains why this is a separate field rather than overloading
// Confidence.
const (
	ResultSourceSchema      = "schema"
	ResultSourceRawFallback = "raw_fallback"
)

// askFallbackMinLength is the minimum rune count below which raw stdout is
// rejected as a fallback answer. Syntactic gate only; no content
// classification. Spec §Syntactic Check.
const askFallbackMinLength = 10

// AskResult is the parsed ===ASK_RESULT=== JSON, plus a transport-quality
// marker (ResultSource) populated by ParseAskOutput.
type AskResult struct {
	Answer       string `json:"answer"`
	Confidence   string `json:"confidence,omitempty"`
	ResultSource string `json:"-"`
}

// ParseAskOutput extracts the ASK_RESULT marker block and unmarshals its
// JSON body. Walks markers last-first so the opencode fence pattern
// (payload sandwiched between opening and closing markers) is recovered.
// Rejects empty answers.
//
// On marker-missing-entirely + raw output passing the syntactic gate,
// returns the raw output as the answer with
// ResultSource="raw_fallback". Marker-present failures (malformed JSON,
// empty answer) intentionally do NOT fall back; spec §Design Decisions
// Resolved #2.
func ParseAskOutput(output string) (AskResult, error) {
	output = strings.TrimSpace(output)
	segments := segmentAfterMarker(output, askMarker)
	if len(segments) == 0 {
		if looksLikePlainAnswer(output) {
			return AskResult{Answer: output, ResultSource: ResultSourceRawFallback}, nil
		}
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
		r.ResultSource = ResultSourceSchema
		return r, nil
	}
	return AskResult{}, lastErr
}

// looksLikePlainAnswer is the syntactic gate for the missing-marker
// fallback. Not a content classifier — the handler layer attaches a
// transparency banner regardless.
func looksLikePlainAnswer(s string) bool {
	return utf8.RuneCountInString(strings.TrimSpace(s)) >= askFallbackMinLength
}
