package queue

import (
	"context"
	"sync"
)

type InMemAttachmentStore struct {
	mu    sync.Mutex
	ready map[string]chan []AttachmentReady
}

func NewInMemAttachmentStore() *InMemAttachmentStore {
	return &InMemAttachmentStore{
		ready: make(map[string]chan []AttachmentReady),
	}
}

func (s *InMemAttachmentStore) Prepare(ctx context.Context, jobID string, attachments []AttachmentMeta) error {
	s.mu.Lock()
	ch, ok := s.ready[jobID]
	if !ok {
		ch = make(chan []AttachmentReady, 1)
		s.ready[jobID] = ch
	}
	s.mu.Unlock()
	result := make([]AttachmentReady, len(attachments))
	for i, a := range attachments {
		result[i] = AttachmentReady{Filename: a.Filename, URL: ""}
	}
	ch <- result
	return nil
}

func (s *InMemAttachmentStore) Resolve(ctx context.Context, jobID string) ([]AttachmentReady, error) {
	s.mu.Lock()
	ch, ok := s.ready[jobID]
	if !ok {
		ch = make(chan []AttachmentReady, 1)
		s.ready[jobID] = ch
	}
	s.mu.Unlock()
	select {
	case result := <-ch:
		return result, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *InMemAttachmentStore) Cleanup(ctx context.Context, jobID string) error {
	s.mu.Lock()
	delete(s.ready, jobID)
	s.mu.Unlock()
	return nil
}
