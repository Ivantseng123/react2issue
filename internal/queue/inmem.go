package queue

import (
	"container/heap"
	"context"
	"sync"
	"sync/atomic"
)

type InMemTransport struct {
	mu         sync.Mutex
	cond       *sync.Cond
	pq         priorityQueue
	capacity   int
	seqCounter atomic.Uint64
	store      JobStore

	jobCh    chan *Job
	resultCh chan *JobResult
	closed   chan struct{}

	attachMu    sync.Mutex
	attachReady map[string]chan []AttachmentReady

	workerMu sync.Mutex
	workers  map[string]WorkerInfo
}

func NewInMemTransport(capacity int, store JobStore) *InMemTransport {
	t := &InMemTransport{
		capacity:    capacity,
		store:       store,
		jobCh:       make(chan *Job, capacity),
		resultCh:    make(chan *JobResult, capacity),
		closed:      make(chan struct{}),
		attachReady: make(map[string]chan []AttachmentReady),
		workers:     make(map[string]WorkerInfo),
	}
	t.cond = sync.NewCond(&t.mu)
	heap.Init(&t.pq)
	go t.dispatchLoop()
	return t
}

func (t *InMemTransport) dispatchLoop() {
	for {
		t.mu.Lock()
		for t.pq.Len() == 0 {
			select {
			case <-t.closed:
				t.mu.Unlock()
				return
			default:
			}
			t.cond.Wait()
		}
		entry := heap.Pop(&t.pq).(*queueEntry)
		t.mu.Unlock()

		select {
		case t.jobCh <- entry.job:
		case <-t.closed:
			return
		}
	}
}

func (t *InMemTransport) Submit(ctx context.Context, job *Job) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.pq.Len() >= t.capacity {
		return ErrQueueFull
	}
	job.Seq = t.seqCounter.Add(1)
	heap.Push(&t.pq, &queueEntry{job: job})
	t.store.Put(job)
	t.cond.Signal()
	return nil
}

func (t *InMemTransport) QueuePosition(jobID string) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	pos := t.pq.position(jobID)
	return pos, nil
}

func (t *InMemTransport) QueueDepth() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.pq.Len()
}

func (t *InMemTransport) Receive(ctx context.Context) (<-chan *Job, error) {
	return t.jobCh, nil
}

func (t *InMemTransport) Ack(ctx context.Context, jobID string) error {
	t.store.UpdateStatus(jobID, JobPreparing)
	t.attachMu.Lock()
	if _, exists := t.attachReady[jobID]; !exists {
		t.attachReady[jobID] = make(chan []AttachmentReady, 1)
	}
	t.attachMu.Unlock()
	return nil
}

func (t *InMemTransport) Register(ctx context.Context, info WorkerInfo) error {
	t.workerMu.Lock()
	defer t.workerMu.Unlock()
	t.workers[info.WorkerID] = info
	return nil
}

func (t *InMemTransport) Unregister(ctx context.Context, workerID string) error {
	t.workerMu.Lock()
	defer t.workerMu.Unlock()
	delete(t.workers, workerID)
	return nil
}

func (t *InMemTransport) ListWorkers(ctx context.Context) ([]WorkerInfo, error) {
	t.workerMu.Lock()
	defer t.workerMu.Unlock()
	result := make([]WorkerInfo, 0, len(t.workers))
	for _, w := range t.workers {
		result = append(result, w)
	}
	return result, nil
}

func (t *InMemTransport) Close() error {
	select {
	case <-t.closed:
	default:
		close(t.closed)
		t.cond.Broadcast()
	}
	return nil
}

func (t *InMemTransport) Prepare(ctx context.Context, jobID string, attachments []AttachmentMeta) error {
	t.attachMu.Lock()
	ch, ok := t.attachReady[jobID]
	if !ok {
		ch = make(chan []AttachmentReady, 1)
		t.attachReady[jobID] = ch
	}
	t.attachMu.Unlock()
	ready := make([]AttachmentReady, len(attachments))
	for i, a := range attachments {
		ready[i] = AttachmentReady{Filename: a.Filename, URL: ""}
	}
	ch <- ready
	return nil
}

func (t *InMemTransport) Resolve(ctx context.Context, jobID string) ([]AttachmentReady, error) {
	t.attachMu.Lock()
	ch, ok := t.attachReady[jobID]
	if !ok {
		ch = make(chan []AttachmentReady, 1)
		t.attachReady[jobID] = ch
	}
	t.attachMu.Unlock()
	select {
	case ready := <-ch:
		return ready, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (t *InMemTransport) Cleanup(ctx context.Context, jobID string) error {
	t.attachMu.Lock()
	delete(t.attachReady, jobID)
	t.attachMu.Unlock()
	return nil
}

func (t *InMemTransport) Publish(ctx context.Context, result *JobResult) error {
	select {
	case t.resultCh <- result:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (t *InMemTransport) Subscribe(ctx context.Context) (<-chan *JobResult, error) {
	return t.resultCh, nil
}
