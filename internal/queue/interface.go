package queue

import "context"

type JobQueue interface {
	Submit(ctx context.Context, job *Job) error
	QueuePosition(jobID string) (int, error)
	QueueDepth() int
	Receive(ctx context.Context) (<-chan *Job, error)
	Ack(ctx context.Context, jobID string) error
	Register(ctx context.Context, info WorkerInfo) error
	Unregister(ctx context.Context, workerID string) error
	ListWorkers(ctx context.Context) ([]WorkerInfo, error)
	Close() error
}

type AttachmentStore interface {
	Prepare(ctx context.Context, jobID string, attachments []AttachmentMeta) error
	Resolve(ctx context.Context, jobID string) ([]AttachmentReady, error)
	Cleanup(ctx context.Context, jobID string) error
}

type ResultBus interface {
	Publish(ctx context.Context, result *JobResult) error
	Subscribe(ctx context.Context) (<-chan *JobResult, error)
	Close() error
}

type JobStore interface {
	Put(job *Job) error
	Get(jobID string) (*JobState, error)
	GetByThread(channelID, threadTS string) (*JobState, error)
	ListPending() ([]*JobState, error)
	UpdateStatus(jobID string, status JobStatus) error
	SetWorker(jobID, workerID string) error
	Delete(jobID string) error
}
