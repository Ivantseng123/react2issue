package workflow

import (
	"context"
	"log/slog"
	"testing"

	"github.com/Ivantseng123/agentdock/app/config"
)

func TestAskWorkflow_Type(t *testing.T) {
	w := &AskWorkflow{}
	if w.Type() != "ask" {
		t.Errorf("Type() = %q", w.Type())
	}
}

func TestAskWorkflow_Trigger_ReturnsRepoPrompt(t *testing.T) {
	w, _ := newTestAskWorkflow(t)
	step, err := w.Trigger(context.Background(), TriggerEvent{ChannelID: "C1", ThreadTS: "1.0"}, "what does X do?")
	if err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	if step.Kind != NextStepPostSelector {
		t.Errorf("expected NextStepPostSelector, got %v", step.Kind)
	}
	if len(step.SelectorActions) != 2 {
		t.Errorf("expected 2 actions (attach/skip), got %d", len(step.SelectorActions))
	}
}

func newTestAskWorkflow(t *testing.T) (*AskWorkflow, *fakeSlackPort) {
	t.Helper()
	cfg := &config.Config{}
	config.ApplyDefaults(cfg)
	slack := newFakeSlackPort()
	return NewAskWorkflow(cfg, slack, nil, slog.Default()), slack
}
