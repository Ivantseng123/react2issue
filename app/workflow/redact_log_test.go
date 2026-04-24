package workflow

// Tests verifying that parse-failure log lines redact known secret values.
// Each test builds a minimal workflow with a fake secret in cfg.Secrets,
// calls HandleResult with an output that contains the raw secret value, and
// asserts that the captured log line contains "***" but NOT the raw value.
// A companion regression test confirms that ordinary output (no secret) is
// preserved byte-for-byte.

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/Ivantseng123/agentdock/app/config"
	"github.com/Ivantseng123/agentdock/shared/queue"
)

// captureLogger returns a *slog.Logger that writes text to buf, plus the buf
// itself. The text handler emits key=value pairs that we can grep for.
func captureLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func cfgWithSecret(secret string) *config.Config {
	cfg := &config.Config{}
	config.ApplyDefaults(cfg)
	cfg.Secrets = map[string]string{"MY_SECRET": secret}
	return cfg
}

// ── IssueWorkflow ─────────────────────────────────────────────────────────────

func TestIssueHandleResult_ParseFail_RedactsSecret(t *testing.T) {
	const fakeSecret = "supersecret-ghp-abc123"
	cfg := cfgWithSecret(fakeSecret)
	var buf bytes.Buffer
	w := NewIssueWorkflow(cfg, newFakeSlackPort(), &fakeIssueCreator{}, nil, nil, captureLogger(&buf))

	state := &queue.JobState{Job: &queue.Job{TaskType: "issue"}}
	result := &queue.JobResult{
		Status:    "completed",
		RawOutput: `{"bad json containing ` + fakeSecret + ` inside"}`,
	}
	_ = w.HandleResult(context.Background(), state, result)

	logged := buf.String()
	if strings.Contains(logged, fakeSecret) {
		t.Errorf("log line contains raw secret %q: %s", fakeSecret, logged)
	}
	if !strings.Contains(logged, "***") {
		t.Errorf("log line should contain '***' redaction placeholder: %s", logged)
	}
}

func TestIssueHandleResult_ParseFail_NoSecret_Unchanged(t *testing.T) {
	cfg := cfgWithSecret("supersecret-ghp-abc123")
	var buf bytes.Buffer
	w := NewIssueWorkflow(cfg, newFakeSlackPort(), &fakeIssueCreator{}, nil, nil, captureLogger(&buf))

	const harmlessOutput = "not valid json at all - totally normal debug text"
	state := &queue.JobState{Job: &queue.Job{TaskType: "issue"}}
	result := &queue.JobResult{
		Status:    "completed",
		RawOutput: harmlessOutput,
	}
	_ = w.HandleResult(context.Background(), state, result)

	logged := buf.String()
	if !strings.Contains(logged, harmlessOutput) {
		t.Errorf("harmless output should appear unchanged in log; logged: %s", logged)
	}
}

// ── AskWorkflow ───────────────────────────────────────────────────────────────

func TestAskHandleResult_ParseFail_RedactsSecret(t *testing.T) {
	const fakeSecret = "supersecret-ask-xyz789"
	cfg := cfgWithSecret(fakeSecret)
	var buf bytes.Buffer
	w := NewAskWorkflow(cfg, newFakeSlackPort(), nil, captureLogger(&buf))

	state := &queue.JobState{Job: &queue.Job{TaskType: "ask"}}
	result := &queue.JobResult{
		Status:    "completed",
		RawOutput: `malformed output with token ` + fakeSecret + ` in it`,
	}
	_ = w.HandleResult(context.Background(), state, result)

	logged := buf.String()
	if strings.Contains(logged, fakeSecret) {
		t.Errorf("log line contains raw secret %q: %s", fakeSecret, logged)
	}
	if !strings.Contains(logged, "***") {
		t.Errorf("log line should contain '***' redaction placeholder: %s", logged)
	}
}

func TestAskHandleResult_ParseFail_NoSecret_Unchanged(t *testing.T) {
	cfg := cfgWithSecret("supersecret-ask-xyz789")
	var buf bytes.Buffer
	w := NewAskWorkflow(cfg, newFakeSlackPort(), nil, captureLogger(&buf))

	const harmlessOutput = "just a plain debug string with no secrets"
	state := &queue.JobState{Job: &queue.Job{TaskType: "ask"}}
	result := &queue.JobResult{
		Status:    "completed",
		RawOutput: harmlessOutput,
	}
	_ = w.HandleResult(context.Background(), state, result)

	logged := buf.String()
	if !strings.Contains(logged, harmlessOutput) {
		t.Errorf("harmless output should appear unchanged in log; logged: %s", logged)
	}
}

// ── PRReviewWorkflow ──────────────────────────────────────────────────────────

func TestPRReviewHandleResult_ParseFail_RedactsSecret(t *testing.T) {
	const fakeSecret = "supersecret-pr-review-qwe456"
	cfg := &config.Config{}
	config.ApplyDefaults(cfg)
	tp := true
	cfg.PRReview.Enabled = &tp
	cfg.Secrets = map[string]string{"MY_SECRET": fakeSecret}

	var buf bytes.Buffer
	w := NewPRReviewWorkflow(cfg, newFakeSlackPort(), &fakeGitHubPR{}, nil, captureLogger(&buf))

	state := &queue.JobState{Job: &queue.Job{TaskType: "pr_review", WorkflowArgs: map[string]string{"pr_url": "https://github.com/foo/bar/pull/1"}}}
	result := &queue.JobResult{
		Status:    "completed",
		RawOutput: `malformed output leaking ` + fakeSecret + ` in it`,
	}
	_ = w.HandleResult(context.Background(), state, result)

	logged := buf.String()
	if strings.Contains(logged, fakeSecret) {
		t.Errorf("log line contains raw secret %q: %s", fakeSecret, logged)
	}
	if !strings.Contains(logged, "***") {
		t.Errorf("log line should contain '***' redaction placeholder: %s", logged)
	}
}

func TestPRReviewHandleResult_ParseFail_NoSecret_Unchanged(t *testing.T) {
	cfg := &config.Config{}
	config.ApplyDefaults(cfg)
	tp := true
	cfg.PRReview.Enabled = &tp
	cfg.Secrets = map[string]string{"MY_SECRET": "supersecret-pr-review-qwe456"}

	var buf bytes.Buffer
	w := NewPRReviewWorkflow(cfg, newFakeSlackPort(), &fakeGitHubPR{}, nil, captureLogger(&buf))

	const harmlessOutput = "plain output string with no secrets inside"
	state := &queue.JobState{Job: &queue.Job{TaskType: "pr_review", WorkflowArgs: map[string]string{"pr_url": "https://github.com/foo/bar/pull/1"}}}
	result := &queue.JobResult{
		Status:    "completed",
		RawOutput: harmlessOutput,
	}
	_ = w.HandleResult(context.Background(), state, result)

	logged := buf.String()
	if !strings.Contains(logged, harmlessOutput) {
		t.Errorf("harmless output should appear unchanged in log; logged: %s", logged)
	}
}
