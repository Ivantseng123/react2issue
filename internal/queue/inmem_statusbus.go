package queue

import "context"

type InMemStatusBus struct {
	ch     chan StatusReport
	closed chan struct{}
}

func NewInMemStatusBus(capacity int) *InMemStatusBus {
	return &InMemStatusBus{
		ch:     make(chan StatusReport, capacity),
		closed: make(chan struct{}),
	}
}

func (b *InMemStatusBus) Report(ctx context.Context, report StatusReport) error {
	select {
	case b.ch <- report:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (b *InMemStatusBus) Subscribe(ctx context.Context) (<-chan StatusReport, error) {
	return b.ch, nil
}

func (b *InMemStatusBus) Close() error {
	select {
	case <-b.closed:
	default:
		close(b.closed)
	}
	return nil
}
