package queue

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestInMemTransport_SubmitAndReceive(t *testing.T) {
	tr := NewInMemTransport(10, NewMemJobStore())
	defer tr.Close()
	ctx := context.Background()
	ch, _ := tr.Receive(ctx)
	tr.Submit(ctx, &Job{ID: "j1", Priority: 50, ChannelID: "C1"})
	select {
	case job := <-ch:
		if job.ID != "j1" {
			t.Errorf("got %q, want j1", job.ID)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for job")
	}
}

func TestInMemTransport_PriorityOrdering(t *testing.T) {
	store := NewMemJobStore()
	tr := NewInMemTransport(10, store)
	defer tr.Close()
	ctx := context.Background()
	tr.Submit(ctx, &Job{ID: "low", Priority: 10})
	tr.Submit(ctx, &Job{ID: "high", Priority: 100})
	tr.Submit(ctx, &Job{ID: "mid", Priority: 50})
	ch, _ := tr.Receive(ctx)
	got := (<-ch).ID
	if got != "high" {
		t.Errorf("first = %q, want high", got)
	}
	got = (<-ch).ID
	if got != "mid" {
		t.Errorf("second = %q, want mid", got)
	}
	got = (<-ch).ID
	if got != "low" {
		t.Errorf("third = %q, want low", got)
	}
}

func TestInMemTransport_SubmitFullQueueReturnsError(t *testing.T) {
	tr := NewInMemTransport(1, NewMemJobStore())
	defer tr.Close()
	ctx := context.Background()
	tr.Submit(ctx, &Job{ID: "j1", Priority: 50})
	err := tr.Submit(ctx, &Job{ID: "j2", Priority: 50})
	if err != ErrQueueFull {
		t.Errorf("expected ErrQueueFull, got %v", err)
	}
}

func TestInMemTransport_QueuePositionAndDepth(t *testing.T) {
	tr := NewInMemTransport(10, NewMemJobStore())
	defer tr.Close()
	ctx := context.Background()
	tr.Submit(ctx, &Job{ID: "j1", Priority: 50})
	tr.Submit(ctx, &Job{ID: "j2", Priority: 50})
	tr.Submit(ctx, &Job{ID: "j3", Priority: 100})
	if d := tr.QueueDepth(); d != 3 {
		t.Errorf("depth = %d, want 3", d)
	}
	pos, _ := tr.QueuePosition("j3")
	if pos != 1 {
		t.Errorf("j3 position = %d, want 1", pos)
	}
	pos, _ = tr.QueuePosition("j1")
	if pos != 2 {
		t.Errorf("j1 position = %d, want 2", pos)
	}
}

func TestInMemTransport_SeqAutoAssigned(t *testing.T) {
	tr := NewInMemTransport(10, NewMemJobStore())
	defer tr.Close()
	ctx := context.Background()
	j1 := &Job{ID: "j1", Priority: 50}
	j2 := &Job{ID: "j2", Priority: 50}
	tr.Submit(ctx, j1)
	tr.Submit(ctx, j2)
	if j1.Seq == 0 || j2.Seq == 0 {
		t.Error("Seq should be auto-assigned (non-zero)")
	}
	if j1.Seq >= j2.Seq {
		t.Errorf("j1.Seq=%d should be < j2.Seq=%d", j1.Seq, j2.Seq)
	}
}

func TestInMemTransport_ResultBus(t *testing.T) {
	tr := NewInMemTransport(10, NewMemJobStore())
	defer tr.Close()
	ctx := context.Background()
	ch, _ := tr.Subscribe(ctx)
	tr.Publish(ctx, &JobResult{JobID: "j1", Status: "completed", Title: "test"})
	select {
	case r := <-ch:
		if r.JobID != "j1" {
			t.Errorf("got %q, want j1", r.JobID)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for result")
	}
}

func TestInMemTransport_ConcurrentSubmitReceive(t *testing.T) {
	tr := NewInMemTransport(100, NewMemJobStore())
	defer tr.Close()
	ctx := context.Background()
	ch, _ := tr.Receive(ctx)

	var wg sync.WaitGroup
	n := 20
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(id int) {
			defer wg.Done()
			tr.Submit(ctx, &Job{ID: fmt.Sprintf("j%d", id), Priority: 50})
		}(i)
	}

	received := 0
	done := make(chan struct{})
	go func() {
		for range ch {
			received++
			if received == n {
				close(done)
				return
			}
		}
	}()

	wg.Wait()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("only received %d/%d jobs", received, n)
	}
}
