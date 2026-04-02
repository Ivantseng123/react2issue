package slack

import (
	"log/slog"
	"sync"
	"time"
)

type dedup struct {
	mu   sync.Mutex
	seen map[string]time.Time
	ttl  time.Duration
}

func newDedup(ttl time.Duration) *dedup {
	d := &dedup{
		seen: make(map[string]time.Time),
		ttl:  ttl,
	}
	go d.cleanup()
	return d
}

func (d *dedup) isDuplicate(eventID string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()

	if t, ok := d.seen[eventID]; ok && time.Since(t) < d.ttl {
		return true
	}
	d.seen[eventID] = time.Now()
	return false
}

func (d *dedup) cleanup() {
	ticker := time.NewTicker(d.ttl)
	defer ticker.Stop()
	for range ticker.C {
		d.mu.Lock()
		now := time.Now()
		for id, t := range d.seen {
			if now.Sub(t) > d.ttl {
				delete(d.seen, id)
			}
		}
		d.mu.Unlock()
	}
}

type ReactionEvent struct {
	EventID   string
	Reaction  string
	ChannelID string
	MessageTS string
	UserID    string
}

type Handler struct {
	dedup     *dedup
	semaphore chan struct{}
	onEvent   func(event ReactionEvent)
}

func NewHandler(maxConcurrent int, dedupTTL time.Duration, onEvent func(ReactionEvent)) *Handler {
	return &Handler{
		dedup:     newDedup(dedupTTL),
		semaphore: make(chan struct{}, maxConcurrent),
		onEvent:   onEvent,
	}
}

func (h *Handler) HandleReaction(event ReactionEvent) bool {
	if h.dedup.isDuplicate(event.EventID) {
		slog.Debug("skipping duplicate event", "eventID", event.EventID)
		return false
	}

	h.semaphore <- struct{}{}
	go func() {
		defer func() { <-h.semaphore }()
		h.onEvent(event)
	}()
	return true
}
