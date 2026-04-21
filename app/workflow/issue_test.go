package workflow

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/Ivantseng123/agentdock/app/config"
)

func TestIssueWorkflow_Type(t *testing.T) {
	w := &IssueWorkflow{}
	if w.Type() != "issue" {
		t.Errorf("Type() = %q, want issue", w.Type())
	}
}

func TestIssueWorkflow_TriggerWithRepoArg_ShortCircuits(t *testing.T) {
	w, _ := newTestIssueWorkflow(t)
	ctx := context.Background()
	ev := TriggerEvent{ChannelID: "C1", ThreadTS: "1.0", UserID: "U1"}

	step, err := w.Trigger(ctx, ev, "foo/bar")
	if err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	if step.Kind == NextStepError {
		t.Errorf("expected non-error NextStep, got error: %q", step.ErrorText)
	}
}

// ── new tests for Task 2.4 ────────────────────────────────────────────────────

func TestIssueWorkflow_Trigger_NoRepoSingleConfigured(t *testing.T) {
	// Single-repo channel config: Trigger should short-circuit the repo picker
	// and return a description prompt — not a repo-selector listing repos.
	w, _ := newTestIssueWorkflow(t, withChannelRepos([]string{"foo/bar"}))
	ev := TriggerEvent{ChannelID: "C1", ThreadTS: "1.0", UserID: "U1"}

	step, err := w.Trigger(context.Background(), ev, "")
	if err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	// Must not be an error.
	if step.Kind == NextStepError {
		t.Fatalf("unexpected error step: %q", step.ErrorText)
	}
	// The description prompt IS a PostSelector, so we cannot assert "not
	// NextStepPostSelector". Instead assert it doesn't look like a repo picker:
	// — it must not list the configured repo as a selector option, and
	// — its prompt must not contain "Which repo".
	for _, a := range step.SelectorActions {
		if a.Value == "foo/bar" {
			t.Errorf("single-repo channel should skip repo selector, but got repo %q as option", a.Value)
		}
	}
	if strings.Contains(step.SelectorPrompt, "Which repo") {
		t.Errorf("single-repo channel should not show repo picker, got prompt: %q", step.SelectorPrompt)
	}
}

func TestIssueWorkflow_Trigger_MultiRepoShowsSelector(t *testing.T) {
	w, _ := newTestIssueWorkflow(t, withChannelRepos([]string{"foo/bar", "baz/qux"}))
	ev := TriggerEvent{ChannelID: "C1", ThreadTS: "1.0", UserID: "U1"}

	step, err := w.Trigger(context.Background(), ev, "")
	if err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	if step.Kind != NextStepPostSelector {
		t.Errorf("expected NextStepPostSelector, got %v", step.Kind)
	}
	if len(step.SelectorActions) != 2 {
		t.Errorf("expected 2 selector options, got %d", len(step.SelectorActions))
	}
}

func TestIssueWorkflow_Selection_RepoPhase_TransitionsToBranchOrDescription(t *testing.T) {
	// After picking a repo, workflow transitions to branch selector (if
	// multi-branch) or description prompt (if single/no branch list).
	w, _ := newTestIssueWorkflow(t)
	p := &Pending{Phase: "repo", State: &issueState{}, ChannelID: "C1", ThreadTS: "1.0"}

	step, err := w.Selection(context.Background(), p, "foo/bar")
	if err != nil {
		t.Fatalf("Selection: %v", err)
	}
	if step.Kind == NextStepError {
		t.Errorf("unexpected error: %q", step.ErrorText)
	}
}

func TestIssueWorkflow_BuildJob_SetsTaskType(t *testing.T) {
	w, _ := newTestIssueWorkflow(t)
	p := &Pending{
		ChannelID: "C1", ThreadTS: "1.0", UserID: "U1",
		State: &issueState{SelectedRepo: "foo/bar", SelectedBranch: "main"},
	}

	job, status, err := w.BuildJob(context.Background(), p)
	if err != nil {
		t.Fatalf("BuildJob: %v", err)
	}
	if job.TaskType != "issue" {
		t.Errorf("TaskType = %q, want issue", job.TaskType)
	}
	if job.Repo != "foo/bar" {
		t.Errorf("Repo = %q", job.Repo)
	}
	if job.Branch != "main" {
		t.Errorf("Branch = %q", job.Branch)
	}
	if job.PromptContext == nil || job.PromptContext.Goal == "" {
		t.Error("PromptContext.Goal must be populated (from config or default)")
	}
	if status == "" {
		t.Error("status text should be non-empty; spec says :mag: 分析 codebase 中...")
	}
}

// ── test helpers ─────────────────────────────────────────────────────────────

type issueOpt func(*config.Config)

func withChannelRepos(repos []string) issueOpt {
	return func(c *config.Config) {
		c.ChannelDefaults.Repos = repos
	}
}

func newTestIssueWorkflow(t *testing.T, opts ...issueOpt) (*IssueWorkflow, *fakeSlackPort) {
	t.Helper()
	cfg := &config.Config{}
	config.ApplyDefaults(cfg) // populates Prompt.Issue defaults
	for _, o := range opts {
		o(cfg)
	}
	slack := newFakeSlackPort()
	w := NewIssueWorkflow(cfg, slack, &fakeIssueCreator{}, nil, nil, slog.Default())
	return w, slack
}
