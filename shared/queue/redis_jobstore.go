package queue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisJobStore persists JobState in Redis so in-flight work survives app
// restarts — the missing piece that caused Slack threads to hang with the
// hourglass forever after a redeploy (issue #123).
//
// Key scheme (prefix defaults to "jobstore"):
//
//	jobstore:<jobID>                          → JSON(JobState)
//	jobstore:thread:<channel>:<threadTS>     → jobID (secondary index for
//	                                             GetByThread — reverse lookup
//	                                             without SCAN on the hot path)
//
// Both keys share the same TTL, refreshed on every write so a long-running
// job doesn't expire mid-flight. Terminal states (Completed/Failed/Cancelled)
// are left to expire naturally rather than deleted eagerly — the ResultListener
// still needs Get() to succeed for its one final HandleResult lookup, and
// eager delete races that read.
//
// Read-modify-write operations (UpdateStatus, SetWorker, SetAgentStatus) use
// WATCH/MULTI/EXEC with bounded retry so concurrent updates from multiple app
// pods (or even the same pod's ResultListener + StatusListener + cancel
// handler) don't lose each other. Lua would also work; WATCH was picked to
// keep the JSON round-trip inside Go — easier to evolve JobState fields
// without editing an embedded script.
type RedisJobStore struct {
	rdb    *redis.Client
	prefix string
	ttl    time.Duration
}

// NewRedisJobStore wires a Redis-backed JobStore. prefix is the string
// prepended to every key (typically "jobstore"); ttl bounds how long a
// JobState survives after its last write. Both app and worker can hold a
// handle concurrently — all writes are atomic.
func NewRedisJobStore(rdb *redis.Client, prefix string, ttl time.Duration) *RedisJobStore {
	if prefix == "" {
		prefix = "jobstore"
	}
	if ttl <= 0 {
		ttl = 1 * time.Hour
	}
	return &RedisJobStore{rdb: rdb, prefix: prefix, ttl: ttl}
}

func (s *RedisJobStore) jobKey(jobID string) string {
	return s.prefix + ":" + jobID
}

func (s *RedisJobStore) threadKey(channelID, threadTS string) string {
	return s.prefix + ":thread:" + channelID + ":" + threadTS
}

// matchPattern is the glob fed to SCAN for listing jobs. Excludes the
// "thread:" secondary index by requiring the jobID segment not start with
// "thread".
func (s *RedisJobStore) matchPattern() string {
	return s.prefix + ":*"
}

// maxWatchRetries bounds the WATCH/MULTI/EXEC retry loop. Hitting this
// means the key is being hammered hard enough that optimistic concurrency
// can't converge — surface the error rather than loop forever.
const maxWatchRetries = 10

// persist writes state to Redis with TTL and refreshes the thread index.
// Caller holds logical ownership of the state struct.
func (s *RedisJobStore) persist(ctx context.Context, state *JobState) error {
	data, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("marshal job state: %w", err)
	}
	pipe := s.rdb.TxPipeline()
	pipe.Set(ctx, s.jobKey(state.Job.ID), data, s.ttl)
	if state.Job.ChannelID != "" && state.Job.ThreadTS != "" {
		pipe.Set(ctx, s.threadKey(state.Job.ChannelID, state.Job.ThreadTS), state.Job.ID, s.ttl)
	}
	_, err = pipe.Exec(ctx)
	return err
}

// Put writes a fresh JobState (Status=JobPending) for a newly-submitted job.
func (s *RedisJobStore) Put(job *Job) error {
	ctx := context.Background()
	state := &JobState{Job: job, Status: JobPending}
	return s.persist(ctx, state)
}

// Get fetches the current state. Returns an error if the key is missing or
// expired — callers (ResultListener, QueuePosition) already handle "not
// found" gracefully.
func (s *RedisJobStore) Get(jobID string) (*JobState, error) {
	ctx := context.Background()
	raw, err := s.rdb.Get(ctx, s.jobKey(jobID)).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, fmt.Errorf("job %q not found", jobID)
		}
		return nil, fmt.Errorf("redis get: %w", err)
	}
	var state JobState
	if err := json.Unmarshal([]byte(raw), &state); err != nil {
		return nil, fmt.Errorf("unmarshal job state: %w", err)
	}
	return &state, nil
}

// GetByThread resolves a thread's job via the secondary index then Get.
// Two round-trips; still O(1) and avoids SCAN on a lookup that happens on
// every @-mention reply.
func (s *RedisJobStore) GetByThread(channelID, threadTS string) (*JobState, error) {
	ctx := context.Background()
	jobID, err := s.rdb.Get(ctx, s.threadKey(channelID, threadTS)).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, fmt.Errorf("no job found for thread %s:%s", channelID, threadTS)
		}
		return nil, fmt.Errorf("redis get thread index: %w", err)
	}
	return s.Get(jobID)
}

// ListPending scans all jobstore keys and returns JobStates currently in
// JobPending. SCAN (not KEYS) so we don't block Redis for other clients.
func (s *RedisJobStore) ListPending() ([]*JobState, error) {
	states, err := s.scanAll()
	if err != nil {
		return nil, err
	}
	var out []*JobState
	for _, st := range states {
		if st.Status == JobPending {
			out = append(out, st)
		}
	}
	return out, nil
}

// ListAll scans all jobstore keys and returns every JobState. SCAN-based,
// safe to call on startup for rehydrate logging.
func (s *RedisJobStore) ListAll() ([]*JobState, error) {
	return s.scanAll()
}

// scanAll iterates the prefix via SCAN, skipping the secondary-index keys
// (jobstore:thread:*) and any entry that no longer decodes. Count=100 is a
// balance between round-trips and per-call work; not tuned.
func (s *RedisJobStore) scanAll() ([]*JobState, error) {
	ctx := context.Background()
	threadPrefix := s.prefix + ":thread:"
	var states []*JobState
	iter := s.rdb.Scan(ctx, 0, s.matchPattern(), 100).Iterator()
	for iter.Next(ctx) {
		key := iter.Val()
		if len(key) > len(threadPrefix) && key[:len(threadPrefix)] == threadPrefix {
			continue
		}
		raw, err := s.rdb.Get(ctx, key).Result()
		if err != nil {
			// Key may have expired between SCAN and GET — skip rather than
			// abort the whole listing.
			continue
		}
		var state JobState
		if err := json.Unmarshal([]byte(raw), &state); err != nil {
			continue
		}
		states = append(states, &state)
	}
	if err := iter.Err(); err != nil {
		return nil, fmt.Errorf("scan jobs: %w", err)
	}
	return states, nil
}

// mutate runs fn under WATCH/MULTI/EXEC with bounded retry. fn receives the
// current JobState (decoded) and may modify it in place; on successful EXEC
// the updated state is persisted with a refreshed TTL. Returns redis.Nil's
// wrapped "not found" error when the key is missing.
func (s *RedisJobStore) mutate(jobID string, fn func(state *JobState) error) error {
	ctx := context.Background()
	key := s.jobKey(jobID)

	for attempt := 0; attempt < maxWatchRetries; attempt++ {
		err := s.rdb.Watch(ctx, func(tx *redis.Tx) error {
			raw, err := tx.Get(ctx, key).Result()
			if err != nil {
				if errors.Is(err, redis.Nil) {
					return fmt.Errorf("job %q not found", jobID)
				}
				return err
			}
			var state JobState
			if err := json.Unmarshal([]byte(raw), &state); err != nil {
				return fmt.Errorf("unmarshal job state: %w", err)
			}
			if err := fn(&state); err != nil {
				return err
			}
			data, err := json.Marshal(&state)
			if err != nil {
				return fmt.Errorf("marshal job state: %w", err)
			}
			_, err = tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
				pipe.Set(ctx, key, data, s.ttl)
				if state.Job != nil && state.Job.ChannelID != "" && state.Job.ThreadTS != "" {
					pipe.Set(ctx, s.threadKey(state.Job.ChannelID, state.Job.ThreadTS), state.Job.ID, s.ttl)
				}
				return nil
			})
			return err
		}, key)

		if err == nil {
			return nil
		}
		// TxFailed signals a concurrent write — retry the whole read-modify
		// cycle with the newer value.
		if errors.Is(err, redis.TxFailedErr) {
			continue
		}
		return err
	}
	return fmt.Errorf("job %q: too many concurrent updates", jobID)
}

// UpdateStatus transitions a job's status. Mirrors MemJobStore: stamps
// StartedAt/WaitTime on the first Running transition and CancelledAt on the
// first Cancelled transition.
func (s *RedisJobStore) UpdateStatus(jobID string, status JobStatus) error {
	return s.mutate(jobID, func(state *JobState) error {
		state.Status = status
		if status == JobRunning && state.StartedAt.IsZero() {
			state.StartedAt = time.Now()
			state.WaitTime = state.StartedAt.Sub(state.Job.SubmittedAt)
		}
		if status == JobCancelled && state.CancelledAt.IsZero() {
			state.CancelledAt = time.Now()
		}
		return nil
	})
}

// SetWorker records which worker picked up the job.
func (s *RedisJobStore) SetWorker(jobID, workerID string) error {
	return s.mutate(jobID, func(state *JobState) error {
		state.WorkerID = workerID
		return nil
	})
}

// SetAgentStatus attaches the latest StatusReport. Matches MemJobStore: a
// missing job is silently ignored (it may have been deleted between the
// worker's publish and the app's consume).
func (s *RedisJobStore) SetAgentStatus(jobID string, report StatusReport) error {
	err := s.mutate(jobID, func(state *JobState) error {
		state.AgentStatus = &report
		return nil
	})
	if err != nil && isNotFoundErr(err) {
		return nil
	}
	return err
}

// Delete removes the job and its thread-index entry. Called by cleanup paths
// and by the watchdog when a job is hard-killed.
func (s *RedisJobStore) Delete(jobID string) error {
	ctx := context.Background()

	// Fetch first to know the thread keys to clear. A missing job is a no-op.
	state, err := s.Get(jobID)
	if err != nil {
		if isNotFoundErr(err) {
			return nil
		}
		return err
	}
	pipe := s.rdb.TxPipeline()
	pipe.Del(ctx, s.jobKey(jobID))
	if state.Job != nil && state.Job.ChannelID != "" && state.Job.ThreadTS != "" {
		pipe.Del(ctx, s.threadKey(state.Job.ChannelID, state.Job.ThreadTS))
	}
	_, err = pipe.Exec(ctx)
	return err
}

// isNotFoundErr reports whether an error came from our "job %q not found"
// wrapper. Keeps string matching in one place.
func isNotFoundErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	// Simple prefix+suffix check; all our callers format "job %q not found".
	return len(msg) > len("not found") && msg[len(msg)-len("not found"):] == "not found"
}
