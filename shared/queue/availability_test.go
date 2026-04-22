package queue_test

import (
	"context"
	"testing"
	"time"

	"github.com/Ivantseng123/agentdock/shared/queue"
	"github.com/Ivantseng123/agentdock/shared/queue/queuetest"
)

func newAvail(t *testing.T) (*queuetest.JobQueue, queue.JobStore, queue.WorkerAvailability) {
	t.Helper()
	store := queue.NewMemJobStore()
	q := queuetest.NewJobQueue(50, store)
	a := queue.NewWorkerAvailability(q, store, queue.AvailabilityConfig{
		AvgJobDuration: 3 * time.Minute,
	})
	return q, store, a
}

func TestAvailability_HealthyOK(t *testing.T) {
	q, _, a := newAvail(t)

	q.Register(context.Background(), queue.WorkerInfo{WorkerID: "w1", Slots: 1})
	q.Register(context.Background(), queue.WorkerInfo{WorkerID: "w2", Slots: 1})

	v := a.CheckHard(context.Background())
	if v.Kind != queue.VerdictOK {
		t.Errorf("Kind = %q, want %q", v.Kind, queue.VerdictOK)
	}
	if v.WorkerCount != 2 {
		t.Errorf("WorkerCount = %d, want 2", v.WorkerCount)
	}
	if v.TotalSlots != 2 {
		t.Errorf("TotalSlots = %d, want 2", v.TotalSlots)
	}
}

func TestAvailability_NoWorkers(t *testing.T) {
	_, _, a := newAvail(t)

	v := a.CheckHard(context.Background())
	if v.Kind != queue.VerdictNoWorkers {
		t.Errorf("Kind = %q, want %q", v.Kind, queue.VerdictNoWorkers)
	}
	if v.WorkerCount != 0 {
		t.Errorf("WorkerCount = %d, want 0", v.WorkerCount)
	}
}
