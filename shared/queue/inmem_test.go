package queue

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestInMemJobQueue_SubmitAndReceive(t *testing.T) {
	bundle := NewInMemBundle(10, 3, NewMemJobStore())
	defer bundle.Close()
	ctx := context.Background()
	ch, _ := bundle.Queue.Receive(ctx)
	bundle.Queue.Submit(ctx, &Job{ID: "j1", Priority: 50, ChannelID: "C1"})
	select {
	case job := <-ch:
		if job.ID != "j1" {
			t.Errorf("got %q, want j1", job.ID)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for job")
	}
}

func TestInMemJobQueue_SeqAutoAssigned(t *testing.T) {
	bundle := NewInMemBundle(10, 3, NewMemJobStore())
	defer bundle.Close()
	ctx := context.Background()
	j1 := &Job{ID: "j1", Priority: 50}
	j2 := &Job{ID: "j2", Priority: 50}
	bundle.Queue.Submit(ctx, j1)
	bundle.Queue.Submit(ctx, j2)
	if j1.Seq == 0 || j2.Seq == 0 {
		t.Error("Seq should be auto-assigned (non-zero)")
	}
	if j1.Seq >= j2.Seq {
		t.Errorf("j1.Seq=%d should be < j2.Seq=%d", j1.Seq, j2.Seq)
	}
}

func TestInMemResultBus_PublishAndSubscribe(t *testing.T) {
	bundle := NewInMemBundle(10, 3, NewMemJobStore())
	defer bundle.Close()
	ctx := context.Background()
	ch, _ := bundle.Results.Subscribe(ctx)
	bundle.Results.Publish(ctx, &JobResult{JobID: "j1", Status: "completed", Title: "test"})
	select {
	case r := <-ch:
		if r.JobID != "j1" {
			t.Errorf("got %q, want j1", r.JobID)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for result")
	}
}

func TestInMemJobQueue_ConcurrentSubmitReceive(t *testing.T) {
	bundle := NewInMemBundle(100, 3, NewMemJobStore())
	defer bundle.Close()
	ctx := context.Background()
	ch, _ := bundle.Queue.Receive(ctx)

	var wg sync.WaitGroup
	n := 20
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(id int) {
			defer wg.Done()
			bundle.Queue.Submit(ctx, &Job{ID: fmt.Sprintf("j%d", id), Priority: 50})
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
