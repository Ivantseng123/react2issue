package queue

import (
	"context"
	"encoding/json"
	"time"

	"github.com/redis/go-redis/v9"
)

const attachmentTTL = 30 * time.Minute

// RedisAttachmentStore implements AttachmentStore using Redis SET/GET with polling.
type RedisAttachmentStore struct {
	client *redis.Client
}

func NewRedisAttachmentStore(client *redis.Client) *RedisAttachmentStore {
	return &RedisAttachmentStore{client: client}
}

func (s *RedisAttachmentStore) attachmentKey(jobID string) string {
	return keyPrefix + "jobs:attachments:" + jobID
}

func (s *RedisAttachmentStore) Prepare(ctx context.Context, jobID string, attachments []AttachmentMeta) error {
	result := make([]AttachmentReady, len(attachments))
	for i, a := range attachments {
		result[i] = AttachmentReady{Filename: a.Filename, URL: ""}
	}

	data, err := json.Marshal(result)
	if err != nil {
		return err
	}

	return s.client.Set(ctx, s.attachmentKey(jobID), data, attachmentTTL).Err()
}

func (s *RedisAttachmentStore) Resolve(ctx context.Context, jobID string) ([]AttachmentReady, error) {
	key := s.attachmentKey(jobID)

	for {
		data, err := s.client.Get(ctx, key).Bytes()
		if err == nil {
			var result []AttachmentReady
			if err := json.Unmarshal(data, &result); err != nil {
				return nil, err
			}
			return result, nil
		}

		if err != redis.Nil {
			return nil, err
		}

		// Key not found — wait 500ms and retry, respecting context cancellation.
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func (s *RedisAttachmentStore) Cleanup(ctx context.Context, jobID string) error {
	return s.client.Del(ctx, s.attachmentKey(jobID)).Err()
}
