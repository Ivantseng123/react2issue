package workflow

import "testing"

func TestParseReviewOutput_Posted(t *testing.T) {
	out := "===REVIEW_RESULT===\n" + `{
  "status": "POSTED",
  "summary": "LGTM with minor nits",
  "comments_posted": 3,
  "comments_skipped": 1,
  "severity_summary": "minor"
}`
	r, err := ParseReviewOutput(out)
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != "POSTED" || r.CommentsPosted != 3 || r.Severity != "minor" {
		t.Errorf("got %+v", r)
	}
}

func TestParseReviewOutput_Skipped(t *testing.T) {
	out := "===REVIEW_RESULT===\n" + `{"status": "SKIPPED", "summary": "lockfile only", "reason": "lockfile_only"}`
	r, err := ParseReviewOutput(out)
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != "SKIPPED" || r.Reason != "lockfile_only" {
		t.Errorf("got %+v", r)
	}
}

func TestParseReviewOutput_Error(t *testing.T) {
	out := "===REVIEW_RESULT===\n" + `{"status": "ERROR", "error": "422 invalid head sha"}`
	r, err := ParseReviewOutput(out)
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != "ERROR" || r.Error != "422 invalid head sha" {
		t.Errorf("got %+v", r)
	}
}

func TestParseReviewOutput_UnknownStatus(t *testing.T) {
	_, err := ParseReviewOutput("===REVIEW_RESULT===\n" + `{"status": "NOPE"}`)
	if err == nil {
		t.Error("unknown status must error")
	}
}

// TestParseReviewOutput_FenceMarkers guards the opencode fence pattern:
// opencode wraps the JSON payload between an opening and closing marker,
// which made LastIndex-only parsing see an empty body after the final
// marker and fail with "unexpected end of JSON input". Payload is copied
// from the 2026-04-24 prod failure on softleader/jasmine-policy#2925.
func TestParseReviewOutput_FenceMarkers(t *testing.T) {
	out := "===REVIEW_RESULT===\n" +
		`{"status": "POSTED", "summary": "new field ok; unrelated version rollback detected", "comments_posted": 1, "comments_skipped": 3, "severity_summary": "major"}` +
		"\n===REVIEW_RESULT==="
	r, err := ParseReviewOutput(out)
	if err != nil {
		t.Fatalf("fence pattern must parse: %v", err)
	}
	if r.Status != "POSTED" || r.CommentsPosted != 1 || r.CommentsSkipped != 3 || r.Severity != "major" {
		t.Errorf("got %+v", r)
	}
}
