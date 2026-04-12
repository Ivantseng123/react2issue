package queue

import "context"

// Coordinator implements JobQueue by routing Submit calls to the appropriate
// queue based on Job.TaskType. All other methods delegate to a fallback queue.
// This is the Decorator pattern -- callers continue using the JobQueue interface
// unchanged.
type Coordinator struct {
	queues   map[string]JobQueue
	fallback JobQueue
}

// NewCoordinator creates a Coordinator that routes to task-type-specific queues
// and falls back to the given queue for unmatched or empty task types.
func NewCoordinator(fallback JobQueue) *Coordinator {
	return &Coordinator{
		queues:   make(map[string]JobQueue),
		fallback: fallback,
	}
}

// RegisterQueue associates a task type with a specific JobQueue.
func (c *Coordinator) RegisterQueue(taskType string, q JobQueue) {
	c.queues[taskType] = q
}

// Submit routes the job to the queue registered for job.TaskType, or to
// the fallback queue if no match is found.
func (c *Coordinator) Submit(ctx context.Context, job *Job) error {
	if job.TaskType != "" {
		if q, ok := c.queues[job.TaskType]; ok {
			return q.Submit(ctx, job)
		}
	}
	return c.fallback.Submit(ctx, job)
}

// QueuePosition searches all registered queues and the fallback for the job.
func (c *Coordinator) QueuePosition(jobID string) (int, error) {
	for _, q := range c.queues {
		pos, err := q.QueuePosition(jobID)
		if err == nil && pos > 0 {
			return pos, nil
		}
	}
	return c.fallback.QueuePosition(jobID)
}

// QueueDepth returns the sum of depths across all unique queues (deduped).
func (c *Coordinator) QueueDepth() int {
	seen := make(map[JobQueue]bool)
	total := 0
	for _, q := range c.queues {
		if !seen[q] {
			total += q.QueueDepth()
			seen[q] = true
		}
	}
	if !seen[c.fallback] {
		total += c.fallback.QueueDepth()
	}
	return total
}

// Worker-side methods delegate to fallback.

func (c *Coordinator) Receive(ctx context.Context) (<-chan *Job, error) {
	return c.fallback.Receive(ctx)
}

func (c *Coordinator) Ack(ctx context.Context, jobID string) error {
	return c.fallback.Ack(ctx, jobID)
}

func (c *Coordinator) Register(ctx context.Context, info WorkerInfo) error {
	return c.fallback.Register(ctx, info)
}

func (c *Coordinator) Unregister(ctx context.Context, workerID string) error {
	return c.fallback.Unregister(ctx, workerID)
}

func (c *Coordinator) ListWorkers(ctx context.Context) ([]WorkerInfo, error) {
	return c.fallback.ListWorkers(ctx)
}

func (c *Coordinator) Close() error {
	return c.fallback.Close()
}
