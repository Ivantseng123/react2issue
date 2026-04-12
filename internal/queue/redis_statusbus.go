package queue

import (
	"context"
	"encoding/json"

	"github.com/redis/go-redis/v9"
)

const statusChannel = keyPrefix + "jobs:status"

// RedisStatusBus implements StatusBus using Redis Pub/Sub.
type RedisStatusBus struct {
	client *redis.Client
	pubsub *redis.PubSub
}

func NewRedisStatusBus(client *redis.Client) *RedisStatusBus {
	return &RedisStatusBus{client: client}
}

func (b *RedisStatusBus) Report(ctx context.Context, report StatusReport) error {
	data, err := json.Marshal(report)
	if err != nil {
		return err
	}
	return b.client.Publish(ctx, statusChannel, data).Err()
}

func (b *RedisStatusBus) Subscribe(ctx context.Context) (<-chan StatusReport, error) {
	b.pubsub = b.client.Subscribe(ctx, statusChannel)

	// Wait for confirmation that subscription is active.
	if _, err := b.pubsub.Receive(ctx); err != nil {
		b.pubsub.Close()
		return nil, err
	}

	ch := make(chan StatusReport, 64)
	go func() {
		defer close(ch)
		for msg := range b.pubsub.Channel() {
			var report StatusReport
			if err := json.Unmarshal([]byte(msg.Payload), &report); err != nil {
				continue
			}
			// Best-effort: drop if consumer is slow.
			select {
			case ch <- report:
			default:
			}
		}
	}()

	return ch, nil
}

func (b *RedisStatusBus) Close() error {
	if b.pubsub != nil {
		return b.pubsub.Close()
	}
	return nil
}
