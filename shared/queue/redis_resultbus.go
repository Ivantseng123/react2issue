package queue

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

const resultStream = keyPrefix + "jobs:results"

// RedisResultBus implements ResultBus using Redis Streams with consumer groups
// for reliable delivery (results must not be lost).
type RedisResultBus struct {
	client     *redis.Client
	consumerID string
	stop       chan struct{}
}

func NewRedisResultBus(client *redis.Client) *RedisResultBus {
	return &RedisResultBus{
		client:     client,
		consumerID: fmt.Sprintf("app-%d", time.Now().UnixNano()),
		stop:       make(chan struct{}),
	}
}

func (b *RedisResultBus) Publish(ctx context.Context, result *JobResult) error {
	data, err := json.Marshal(result)
	if err != nil {
		return err
	}
	return b.client.XAdd(ctx, &redis.XAddArgs{
		Stream: resultStream,
		Values: map[string]interface{}{"payload": string(data)},
	}).Err()
}

func (b *RedisResultBus) Subscribe(ctx context.Context) (<-chan *JobResult, error) {
	// Create consumer group; ignore BUSYGROUP error if it already exists.
	err := b.client.XGroupCreateMkStream(ctx, resultStream, "app", "0").Err()
	if err != nil && !isRedisError(err, "BUSYGROUP") {
		return nil, err
	}

	ch := make(chan *JobResult, 64)
	go func() {
		defer close(ch)
		for {
			select {
			case <-b.stop:
				return
			default:
			}

			streams, err := b.client.XReadGroup(ctx, &redis.XReadGroupArgs{
				Group:    "app",
				Consumer: b.consumerID,
				Streams:  []string{resultStream, ">"},
				Count:    1,
				Block:    5 * time.Second,
			}).Result()
			if err != nil {
				// Block timeout returns redis.Nil; just retry.
				if err == redis.Nil {
					continue
				}
				// Check if context cancelled or stop signalled.
				select {
				case <-b.stop:
					return
				case <-ctx.Done():
					return
				default:
					continue
				}
			}

			for _, stream := range streams {
				for _, msg := range stream.Messages {
					payload, ok := msg.Values["payload"].(string)
					if !ok {
						// ACK malformed message to avoid redelivery.
						b.client.XAck(ctx, resultStream, "app", msg.ID)
						continue
					}

					var result JobResult
					if err := json.Unmarshal([]byte(payload), &result); err != nil {
						b.client.XAck(ctx, resultStream, "app", msg.ID)
						continue
					}

					select {
					case ch <- &result:
					case <-b.stop:
						return
					case <-ctx.Done():
						return
					}

					// ACK after successful delivery to channel.
					b.client.XAck(ctx, resultStream, "app", msg.ID)
				}
			}
		}
	}()

	return ch, nil
}

func (b *RedisResultBus) Close() error {
	close(b.stop)
	return nil
}

// isRedisError checks if the error message contains the given Redis error prefix.
func isRedisError(err error, prefix string) bool {
	if err == nil {
		return false
	}
	return len(err.Error()) >= len(prefix) && err.Error()[:len(prefix)] == prefix
}
