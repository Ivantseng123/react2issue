package queue

import "github.com/redis/go-redis/v9"

// NewRedisBundle creates a Bundle backed by Redis.
func NewRedisBundle(rdb *redis.Client, store JobStore, taskType string) *Bundle {
	return &Bundle{
		Queue:       NewRedisJobQueue(rdb, store, taskType),
		Results:     NewRedisResultBus(rdb),
		Attachments: NewRedisAttachmentStore(rdb),
		Commands:    NewRedisCommandBus(rdb),
		Status:      NewRedisStatusBus(rdb),
	}
}
