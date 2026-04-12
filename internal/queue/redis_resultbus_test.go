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
		JobID:      "job-456",
		Status:     "created",
		Title:      "Bug: login fails on mobile",
		Labels:     []string{"bug", "mobile"},
		Confidence: "high",
		FilesFound: 3,
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
		if got.Title != result.Title {
			t.Errorf("Title = %q, want %q", got.Title, result.Title)
		}
		if got.Confidence != result.Confidence {
			t.Errorf("Confidence = %q, want %q", got.Confidence, result.Confidence)
		}
		if got.FilesFound != result.FilesFound {
			t.Errorf("FilesFound = %d, want %d", got.FilesFound, result.FilesFound)
		}
		if len(got.Labels) != len(result.Labels) {
			t.Fatalf("Labels len = %d, want %d", len(got.Labels), len(result.Labels))
		}
		for i, l := range got.Labels {
			if l != result.Labels[i] {
				t.Errorf("Labels[%d] = %q, want %q", i, l, result.Labels[i])
			}
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for job result")
	}
}
