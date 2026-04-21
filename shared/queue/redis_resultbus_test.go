package queue

import (
	"context"
	"testing"
	"time"
)

func TestRedisResultBus_PublishAndSubscribe(t *testing.T) {
	client := testRedisClient(t)

	bus := NewRedisResultBus(client)
	defer bus.Close()

	ctx := context.Background()

	ch, err := bus.Subscribe(ctx)
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}

	// Give consumer group reader time to start.
	time.Sleep(100 * time.Millisecond)

	result := &JobResult{
		JobID:     "job-456",
		Status:    "created",
		RawOutput: `{"status":"CREATED","title":"Bug: login fails on mobile"}`,
	}

	if err := bus.Publish(ctx, result); err != nil {
		t.Fatalf("Publish failed: %v", err)
	}

	select {
	case got := <-ch:
		if got.JobID != result.JobID {
			t.Errorf("JobID = %q, want %q", got.JobID, result.JobID)
		}
		if got.Status != result.Status {
			t.Errorf("Status = %q, want %q", got.Status, result.Status)
		}
		if got.RawOutput != result.RawOutput {
			t.Errorf("RawOutput = %q, want %q", got.RawOutput, result.RawOutput)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for job result")
	}
}
