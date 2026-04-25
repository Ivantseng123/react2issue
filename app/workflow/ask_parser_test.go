package workflow

import (
	"strings"
	"testing"
)

func TestParseAskOutput_Valid(t *testing.T) {
	output := "Some thinking...\n\n===ASK_RESULT===\n" + `{"answer":"the answer is 42","confidence":"high"}`
	r, err := ParseAskOutput(output)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if r.Answer != "the answer is 42" {
		t.Errorf("answer = %q", r.Answer)
	}
	if r.Confidence != "high" {
		t.Errorf("confidence = %q", r.Confidence)
	}
	if r.ResultSource != ResultSourceSchema {
		t.Errorf("ResultSource = %q, want %q", r.ResultSource, ResultSourceSchema)
	}
}

// TestParseAskOutput_MarkerMissing_FallbackToRaw exercises the missing-marker
// fallback: when the agent omits the ===ASK_RESULT=== wrapper but its raw
// stdout passes the syntactic gate, the parser surfaces stdout verbatim as
// the answer with ResultSource="raw_fallback". Spec §Ask Fallback Policy.
func TestParseAskOutput_MarkerMissing_FallbackToRaw(t *testing.T) {
	output := "Here is the answer to your question."
	r, err := ParseAskOutput(output)
	if err != nil {
		t.Fatalf("expected fallback success, got error: %v", err)
	}
	if r.Answer != output {
		t.Errorf("Answer = %q, want %q", r.Answer, output)
	}
	if r.ResultSource != ResultSourceRawFallback {
		t.Errorf("ResultSource = %q, want %q", r.ResultSource, ResultSourceRawFallback)
	}
}

func TestParseAskOutput_MarkerMissing_EmptyFails(t *testing.T) {
	if _, err := ParseAskOutput(""); err == nil {
		t.Error("expected error on empty stdout")
	}
}

func TestParseAskOutput_MarkerMissing_WhitespaceOnlyFails(t *testing.T) {
	if _, err := ParseAskOutput("   \n\t  \n"); err == nil {
		t.Error("expected error on whitespace-only stdout")
	}
}

func TestParseAskOutput_MarkerMissing_TooShortFails(t *testing.T) {
	if _, err := ParseAskOutput("hi"); err == nil {
		t.Error("expected error on too-short stdout")
	}
}

func TestParseAskOutput_EmptyAnswer(t *testing.T) {
	output := "===ASK_RESULT===\n" + `{"answer":""}`
	if _, err := ParseAskOutput(output); err == nil {
		t.Error("expected error when answer empty")
	}
}

// TestParseAskOutput_MalformedJSON confirms marker-present-but-unparseable
// JSON still fails closed. Fallback is intentionally limited to
// missing-marker; spec §Design Decisions Resolved #2.
func TestParseAskOutput_MalformedJSON(t *testing.T) {
	output := "===ASK_RESULT===\n{not json"
	if _, err := ParseAskOutput(output); err == nil {
		t.Error("expected error on malformed JSON; fallback must not cover this case")
	}
}

func TestParseAskOutput_MultipleMarkers_LastWins(t *testing.T) {
	output := "===ASK_RESULT===\n" + `{"answer":"first"}` + "\n\n===ASK_RESULT===\n" + `{"answer":"last"}`
	r, err := ParseAskOutput(output)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if !strings.Contains(r.Answer, "last") {
		t.Errorf("expected last marker to win, got %q", r.Answer)
	}
}

// TestParseAskOutput_FenceMarkers guards the opencode fence pattern where
// the payload sits between an opening and closing marker. Without the
// fence-aware walk the last marker's segment is empty and parsing errors
// out before reaching the real payload.
func TestParseAskOutput_FenceMarkers(t *testing.T) {
	output := "===ASK_RESULT===\n" + `{"answer":"real answer","confidence":"high"}` + "\n===ASK_RESULT==="
	r, err := ParseAskOutput(output)
	if err != nil {
		t.Fatalf("fence pattern must parse: %v", err)
	}
	if r.Answer != "real answer" || r.Confidence != "high" {
		t.Errorf("got %+v", r)
	}
}
