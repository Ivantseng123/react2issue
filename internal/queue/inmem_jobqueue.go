package queue

import (
	"container/heap"
	"context"
	"sync"
	"sync/atomic"
)

type InMemJobQueue struct {
	mu         sync.Mutex
	cond       *sync.Cond
	pq         priorityQueue
	capacity   int
	seqCounter atomic.Uint64
	store      JobStore
	jobCh      chan *Job
	closed     chan struct{}

	workerMu sync.Mutex
	workers  map[string]WorkerInfo
}

func NewInMemJobQueue(capacity int, store JobStore) *InMemJobQueue {
	q := &InMemJobQueue{
		capacity: capacity,
		store:    store,
		jobCh:    make(chan *Job, capacity),
		closed:   make(chan struct{}),
		workers:  make(map[string]WorkerInfo),
	}
	q.cond = sync.NewCond(&q.mu)
	heap.Init(&q.pq)
	go q.dispatchLoop()
	return q
}

func (q *InMemJobQueue) dispatchLoop() {
	for {
		q.mu.Lock()
		for q.pq.Len() == 0 {
			select {
			case <-q.closed:
				q.mu.Unlock()
				return
			default:
			}
			q.cond.Wait()
		}
		entry := heap.Pop(&q.pq).(*queueEntry)
		q.mu.Unlock()

		select {
		case q.jobCh <- entry.job:
		case <-q.closed:
			return
		}
	}
}

func (q *InMemJobQueue) Submit(ctx context.Context, job *Job) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.pq.Len() >= q.capacity {
		return ErrQueueFull
	}
	job.Seq = q.seqCounter.Add(1)
	heap.Push(&q.pq, &queueEntry{job: job})
	q.store.Put(job)
	q.cond.Signal()
	return nil
}

func (q *InMemJobQueue) QueuePosition(jobID string) (int, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	pos := q.pq.position(jobID)
	return pos, nil
}

func (q *InMemJobQueue) QueueDepth() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.pq.Len()
}

func (q *InMemJobQueue) Receive(ctx context.Context) (<-chan *Job, error) {
	return q.jobCh, nil
}

func (q *InMemJobQueue) Ack(ctx context.Context, jobID string) error {
	q.store.UpdateStatus(jobID, JobPreparing)
	return nil
}

func (q *InMemJobQueue) Register(ctx context.Context, info WorkerInfo) error {
	q.workerMu.Lock()
	defer q.workerMu.Unlock()
	q.workers[info.WorkerID] = info
	return nil
}

func (q *InMemJobQueue) Unregister(ctx context.Context, workerID string) error {
	q.workerMu.Lock()
	defer q.workerMu.Unlock()
	delete(q.workers, workerID)
	return nil
}

func (q *InMemJobQueue) ListWorkers(ctx context.Context) ([]WorkerInfo, error) {
	q.workerMu.Lock()
	defer q.workerMu.Unlock()
	result := make([]WorkerInfo, 0, len(q.workers))
	for _, w := range q.workers {
		result = append(result, w)
	}
	return result, nil
}

func (q *InMemJobQueue) Close() error {
	select {
	case <-q.closed:
	default:
		close(q.closed)
		q.cond.Broadcast()
	}
	return nil
}
