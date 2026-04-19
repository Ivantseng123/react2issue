package queue

import "context"

type InMemResultBus struct {
	ch     chan *JobResult
	closed chan struct{}
}

func NewInMemResultBus(capacity int) *InMemResultBus {
	return &InMemResultBus{
		ch:     make(chan *JobResult, capacity),
		closed: make(chan struct{}),
	}
}

func (b *InMemResultBus) Publish(ctx context.Context, result *JobResult) error {
	select {
	case b.ch <- result:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (b *InMemResultBus) Subscribe(ctx context.Context) (<-chan *JobResult, error) {
	return b.ch, nil
}

func (b *InMemResultBus) Close() error {
	select {
	case <-b.closed:
	default:
		close(b.closed)
	}
	return nil
}
