package queue

import "context"

type InMemCommandBus struct {
	ch     chan Command
	closed chan struct{}
}

func NewInMemCommandBus(capacity int) *InMemCommandBus {
	return &InMemCommandBus{
		ch:     make(chan Command, capacity),
		closed: make(chan struct{}),
	}
}

func (b *InMemCommandBus) Send(ctx context.Context, cmd Command) error {
	select {
	case b.ch <- cmd:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (b *InMemCommandBus) Receive(ctx context.Context) (<-chan Command, error) {
	return b.ch, nil
}

func (b *InMemCommandBus) Close() error {
	select {
	case <-b.closed:
	default:
		close(b.closed)
	}
	return nil
}
