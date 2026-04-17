package queue

import (
	"context"
	"testing"
	"time"
)

func TestProcessRegistry_KillNotFound(t *testing.T) {
	reg := NewProcessRegistry()
	err := reg.Kill("nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent job")
	}
}

func TestProcessRegistry_RegisterPendingThenKill(t *testing.T) {
	reg := NewProcessRegistry()
	ctx, cancel := context.WithCancel(context.Background())

	reg.RegisterPending("j1", cancel)

	go func() {
		<-ctx.Done()
		time.Sleep(10 * time.Millisecond)
		reg.Remove("j1")
	}()

	if err := reg.Kill("j1"); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	if ctx.Err() == nil {
		t.Error("context should be cancelled after Kill on pending entry")
	}
}

func TestProcessRegistry_SetStartedAfterPending(t *testing.T) {
	reg := NewProcessRegistry()
	_, cancel := context.WithCancel(context.Background())

	reg.RegisterPending("j1", cancel)
	reg.SetStarted("j1", 42, "claude")

	agent, ok := reg.Get("j1")
	if !ok {
		t.Fatal("expected agent entry")
	}
	if agent.PID != 42 {
		t.Errorf("PID = %d, want 42", agent.PID)
	}
	if agent.Command != "claude" {
		t.Errorf("Command = %q, want claude", agent.Command)
	}
	if agent.StartedAt.IsZero() {
		t.Error("StartedAt should be set by SetStarted")
	}
}

func TestProcessRegistry_SetStartedWithoutPendingIsNoop(t *testing.T) {
	reg := NewProcessRegistry()
	reg.SetStarted("unknown", 1, "x") // must not panic
	if _, ok := reg.Get("unknown"); ok {
		t.Error("SetStarted without RegisterPending should not create an entry")
	}
}
