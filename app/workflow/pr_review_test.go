package workflow

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/Ivantseng123/agentdock/app/config"
)

type fakeGitHubPR struct {
	pr      *PullRequest
	err     error
	calledN int
}

func (f *fakeGitHubPR) GetPullRequest(ctx context.Context, owner, repo string, number int) (*PullRequest, error) {
	f.calledN = number
	return f.pr, f.err
}

func TestPRReviewWorkflow_Type(t *testing.T) {
	w := &PRReviewWorkflow{}
	if w.Type() != "pr_review" {
		t.Errorf("Type() = %q", w.Type())
	}
}

func TestPRReviewWorkflow_TriggerAPath_Valid(t *testing.T) {
	pr := &PullRequest{Number: 7, State: "open", Title: "T"}
	pr.Head.Ref = "feature-x"
	pr.Head.SHA = "abc123"
	pr.Head.Repo.FullName = "forker/bar"
	pr.Head.Repo.CloneURL = "https://github.com/forker/bar.git"
	pr.Base.Ref = "main"

	w, _ := newTestPRReviewWorkflow(t)
	w.github = &fakeGitHubPR{pr: pr}

	step, err := w.Trigger(context.Background(), TriggerEvent{ChannelID: "C1", ThreadTS: "1.0"}, "https://github.com/foo/bar/pull/7")
	if err != nil {
		t.Fatal(err)
	}
	if step.Kind != NextStepSubmit {
		t.Errorf("expected NextStepSubmit, got %v", step.Kind)
	}
	st := step.Pending.State.(*prReviewState)
	if st.HeadRepo != "forker/bar" {
		t.Errorf("HeadRepo = %q", st.HeadRepo)
	}
}

func TestPRReviewWorkflow_TriggerAPath_404(t *testing.T) {
	w, slack := newTestPRReviewWorkflow(t)
	w.github = &fakeGitHubPR{err: errors.New("404 not found")}
	step, _ := w.Trigger(context.Background(), TriggerEvent{ChannelID: "C1", ThreadTS: "1.0"}, "https://github.com/foo/bar/pull/999")
	if step.Kind != NextStepError {
		t.Errorf("expected NextStepError, got %v", step.Kind)
	}
	_ = slack
}

func TestPRReviewWorkflow_TriggerAPath_PartialURLRejected(t *testing.T) {
	w, _ := newTestPRReviewWorkflow(t)
	step, _ := w.Trigger(context.Background(), TriggerEvent{ChannelID: "C1", ThreadTS: "1.0"}, "github.com/foo/bar/pull/7")
	if step.Kind != NextStepError {
		t.Errorf("expected NextStepError on partial URL")
	}
}

func TestPRReviewWorkflow_TriggerDisabled(t *testing.T) {
	w, _ := newTestPRReviewWorkflow(t)
	w.cfg.PRReview.Enabled = false
	step, _ := w.Trigger(context.Background(), TriggerEvent{ChannelID: "C1"}, "https://github.com/foo/bar/pull/7")
	if step.Kind != NextStepError {
		t.Errorf("expected NextStepError when feature-flag disabled")
	}
}

func newTestPRReviewWorkflow(t *testing.T) (*PRReviewWorkflow, *fakeSlackPort) {
	t.Helper()
	cfg := &config.Config{}
	config.ApplyDefaults(cfg)
	cfg.PRReview.Enabled = true
	slack := newFakeSlackPort()
	w := NewPRReviewWorkflow(cfg, slack, &fakeGitHubPR{}, nil, slog.Default())
	return w, slack
}
