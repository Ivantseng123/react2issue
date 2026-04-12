package queue

import (
	"context"
	"testing"
	"time"
)

func TestRedisStatusBus_ReportAndSubscribe(t *testing.T) {
	client := testRedisClient(t)

	bus := NewRedisStatusBus(client)
	defer bus.Close()

	ctx := context.Background()

	ch, err := bus.Subscribe(ctx)
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}

	// Wait for subscription to be established.
	time.Sleep(100 * time.Millisecond)

	report := StatusReport{
		JobID:       "job-123",
		WorkerID:    "worker-1",
		PID:         4242,
		AgentCmd:    "claude",
		Alive:       true,
		LastEvent:   "tool_call",
		LastEventAt: time.Now().Truncate(time.Millisecond),
		ToolCalls:   7,
		FilesRead:   3,
		OutputBytes: 1024,
		CostUSD:     0.05,
	}

	if err := bus.Report(ctx, report); err != nil {
		t.Fatalf("Report failed: %v", err)
	}

	select {
	case got := <-ch:
		if got.JobID != report.JobID {
			t.Errorf("JobID = %q, want %q", got.JobID, report.JobID)
		}
		if got.ToolCalls != report.ToolCalls {
			t.Errorf("ToolCalls = %d, want %d", got.ToolCalls, report.ToolCalls)
		}
		if got.WorkerID != report.WorkerID {
			t.Errorf("WorkerID = %q, want %q", got.WorkerID, report.WorkerID)
		}
		if got.PID != report.PID {
			t.Errorf("PID = %d, want %d", got.PID, report.PID)
		}
		if got.Alive != report.Alive {
			t.Errorf("Alive = %v, want %v", got.Alive, report.Alive)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for status report")
	}
}
