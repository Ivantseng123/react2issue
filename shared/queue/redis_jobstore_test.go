package queue

import (
	"sync"
	"testing"
	"time"
)

func newTestRedisJobStore(t *testing.T, ttl time.Duration) *RedisJobStore {
	t.Helper()
	client := testRedisClient(t)
	return NewRedisJobStore(client, "jobstore", ttl)
}

func TestRedisJobStore_PutAndGet(t *testing.T) {
	s := newTestRedisJobStore(t, time.Minute)
	job := &Job{ID: "j1", ChannelID: "C1", ThreadTS: "T1", SubmittedAt: time.Now()}
	if err := s.Put(job); err != nil {
		t.Fatal(err)
	}
	state, err := s.Get("j1")
	if err != nil {
		t.Fatal(err)
	}
	if state.Job.ID != "j1" {
		t.Errorf("ID = %q, want j1", state.Job.ID)
	}
	if state.Status != JobPending {
		t.Errorf("status = %q, want pending", state.Status)
	}
}

func TestRedisJobStore_GetNotFound(t *testing.T) {
	s := newTestRedisJobStore(t, time.Minute)
	if _, err := s.Get("missing"); err == nil {
		t.Error("expected error for missing job")
	}
}

func TestRedisJobStore_GetByThread(t *testing.T) {
	s := newTestRedisJobStore(t, time.Minute)
	if err := s.Put(&Job{ID: "j1", ChannelID: "C1", ThreadTS: "T1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(&Job{ID: "j2", ChannelID: "C2", ThreadTS: "T2"}); err != nil {
		t.Fatal(err)
	}
	state, err := s.GetByThread("C1", "T1")
	if err != nil {
		t.Fatal(err)
	}
	if state.Job.ID != "j1" {
		t.Errorf("got %q, want j1", state.Job.ID)
	}
	if _, err := s.GetByThread("Cx", "Tx"); err == nil {
		t.Error("expected error for missing thread")
	}
}

func TestRedisJobStore_UpdateStatus_StampsStarted(t *testing.T) {
	s := newTestRedisJobStore(t, time.Minute)
	submitted := time.Now().Add(-2 * time.Second)
	if err := s.Put(&Job{ID: "j1", SubmittedAt: submitted}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateStatus("j1", JobRunning); err != nil {
		t.Fatal(err)
	}
	state, _ := s.Get("j1")
	if state.StartedAt.IsZero() {
		t.Fatal("StartedAt should be stamped")
	}
	if state.WaitTime <= 0 {
		t.Errorf("WaitTime = %v, want > 0", state.WaitTime)
	}
}

func TestRedisJobStore_UpdateStatus_StampsCancelled(t *testing.T) {
	s := newTestRedisJobStore(t, time.Minute)
	if err := s.Put(&Job{ID: "j1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateStatus("j1", JobCancelled); err != nil {
		t.Fatal(err)
	}
	state, _ := s.Get("j1")
	if state.CancelledAt.IsZero() {
		t.Error("CancelledAt should be stamped")
	}
}

func TestRedisJobStore_SetWorker(t *testing.T) {
	s := newTestRedisJobStore(t, time.Minute)
	if err := s.Put(&Job{ID: "j1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetWorker("j1", "worker-1"); err != nil {
		t.Fatal(err)
	}
	state, _ := s.Get("j1")
	if state.WorkerID != "worker-1" {
		t.Errorf("WorkerID = %q, want worker-1", state.WorkerID)
	}
}

func TestRedisJobStore_SetAgentStatus(t *testing.T) {
	s := newTestRedisJobStore(t, time.Minute)
	if err := s.Put(&Job{ID: "j1"}); err != nil {
		t.Fatal(err)
	}
	report := StatusReport{JobID: "j1", WorkerID: "w1", Alive: true, ToolCalls: 3}
	if err := s.SetAgentStatus("j1", report); err != nil {
		t.Fatal(err)
	}
	state, _ := s.Get("j1")
	if state.AgentStatus == nil {
		t.Fatal("AgentStatus nil")
	}
	if state.AgentStatus.ToolCalls != 3 {
		t.Errorf("ToolCalls = %d, want 3", state.AgentStatus.ToolCalls)
	}
}

func TestRedisJobStore_SetAgentStatus_MissingJobIsSilent(t *testing.T) {
	s := newTestRedisJobStore(t, time.Minute)
	// MemJobStore silently ignores — RedisJobStore should match.
	if err := s.SetAgentStatus("never-existed", StatusReport{}); err != nil {
		t.Errorf("expected nil for missing job, got %v", err)
	}
}

func TestRedisJobStore_Delete(t *testing.T) {
	s := newTestRedisJobStore(t, time.Minute)
	if err := s.Put(&Job{ID: "j1", ChannelID: "C1", ThreadTS: "T1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete("j1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get("j1"); err == nil {
		t.Error("Get after Delete should fail")
	}
	// Thread index also cleared.
	if _, err := s.GetByThread("C1", "T1"); err == nil {
		t.Error("GetByThread after Delete should fail")
	}
}

func TestRedisJobStore_Delete_MissingIsNoOp(t *testing.T) {
	s := newTestRedisJobStore(t, time.Minute)
	if err := s.Delete("never-existed"); err != nil {
		t.Errorf("Delete missing: %v", err)
	}
}

func TestRedisJobStore_ListPending(t *testing.T) {
	s := newTestRedisJobStore(t, time.Minute)
	if err := s.Put(&Job{ID: "j1", ChannelID: "C1", ThreadTS: "T1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(&Job{ID: "j2", ChannelID: "C2", ThreadTS: "T2"}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateStatus("j2", JobRunning); err != nil {
		t.Fatal(err)
	}
	pending, err := s.ListPending()
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 {
		t.Fatalf("pending len = %d, want 1", len(pending))
	}
	if pending[0].Job.ID != "j1" {
		t.Errorf("pending[0] = %q, want j1", pending[0].Job.ID)
	}
}

func TestRedisJobStore_ListAll(t *testing.T) {
	s := newTestRedisJobStore(t, time.Minute)
	for _, id := range []string{"a", "b", "c"} {
		if err := s.Put(&Job{ID: id, ChannelID: "C" + id, ThreadTS: "T" + id}); err != nil {
			t.Fatal(err)
		}
	}
	all, err := s.ListAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("ListAll len = %d, want 3 (thread index keys must not be counted)", len(all))
	}
}

func TestRedisJobStore_TTLExpiry(t *testing.T) {
	// 1-second TTL: quick enough for unit tests, long enough that the write
	// pipeline can complete.
	s := newTestRedisJobStore(t, 1*time.Second)
	if err := s.Put(&Job{ID: "j1", ChannelID: "C1", ThreadTS: "T1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get("j1"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1500 * time.Millisecond)
	if _, err := s.Get("j1"); err == nil {
		t.Error("expected expiry after TTL")
	}
	if _, err := s.GetByThread("C1", "T1"); err == nil {
		t.Error("thread index should also expire")
	}
}

func TestRedisJobStore_ConcurrentUpdateNoLostWrite(t *testing.T) {
	// Regression guard for the whole reason this store exists atomically:
	// ResultListener, StatusListener and cancel-handler can all hit the
	// same jobID concurrently. A naive get-decode-encode-set would lose
	// writes; WATCH/MULTI/EXEC must serialise them.
	s := newTestRedisJobStore(t, time.Minute)
	if err := s.Put(&Job{ID: "j1", SubmittedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}

	const goroutines = 8
	const iterations = 20

	var wg sync.WaitGroup
	wg.Add(goroutines * 2)

	// Half the workers push SetAgentStatus increments.
	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				// Ramp ToolCalls so the final value must reflect SOME ordering
				// — any lost write shows up as a smaller max than expected.
				_ = s.SetAgentStatus("j1", StatusReport{JobID: "j1", ToolCalls: i + 1})
			}
		}()
	}
	// Other half flip worker id / status, also using the same key.
	for g := 0; g < goroutines; g++ {
		go func(gid int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				_ = s.SetWorker("j1", "worker-concurrent")
				_ = s.UpdateStatus("j1", JobRunning)
			}
		}(g)
	}
	wg.Wait()

	state, err := s.Get("j1")
	if err != nil {
		t.Fatal(err)
	}
	if state.WorkerID != "worker-concurrent" {
		t.Errorf("WorkerID = %q, want worker-concurrent (write lost)", state.WorkerID)
	}
	if state.Status != JobRunning {
		t.Errorf("Status = %q, want running", state.Status)
	}
	if state.AgentStatus == nil || state.AgentStatus.ToolCalls == 0 {
		t.Error("AgentStatus missing — some SetAgentStatus write was silently dropped")
	}
	if state.StartedAt.IsZero() {
		t.Error("StartedAt not stamped despite many JobRunning transitions")
	}
}

func TestRedisJobStore_JSONRoundTrip_PreservesFields(t *testing.T) {
	// Guard against subtle marshal bugs — zero-time.Time, pointer fields
	// (AgentStatus), nested Job pointer. If any of these regress, downstream
	// code that trusts the decoded state breaks.
	s := newTestRedisJobStore(t, time.Minute)
	job := &Job{
		ID:          "rt1",
		ChannelID:   "C",
		ThreadTS:    "T",
		Priority:    3,
		TaskType:    "triage",
		Repo:        "org/repo",
		SubmittedAt: time.Now().UTC().Truncate(time.Millisecond),
	}
	if err := s.Put(job); err != nil {
		t.Fatal(err)
	}
	if err := s.SetWorker("rt1", "worker-x"); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateStatus("rt1", JobRunning); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAgentStatus("rt1", StatusReport{JobID: "rt1", WorkerID: "worker-x", ToolCalls: 5, Alive: true}); err != nil {
		t.Fatal(err)
	}
	state, err := s.Get("rt1")
	if err != nil {
		t.Fatal(err)
	}
	if state.Job.ID != "rt1" || state.Job.Priority != 3 || state.Job.Repo != "org/repo" {
		t.Errorf("job fields mangled: %+v", state.Job)
	}
	if state.WorkerID != "worker-x" {
		t.Errorf("WorkerID = %q", state.WorkerID)
	}
	if state.Status != JobRunning {
		t.Errorf("Status = %q", state.Status)
	}
	if state.AgentStatus == nil || state.AgentStatus.ToolCalls != 5 {
		t.Errorf("AgentStatus not preserved: %+v", state.AgentStatus)
	}
	if state.StartedAt.IsZero() {
		t.Error("StartedAt lost in round-trip")
	}
	// CancelledAt is zero-valued here — verify it round-trips as zero, not
	// as a garbage encoded value.
	if !state.CancelledAt.IsZero() {
		t.Errorf("CancelledAt should be zero, got %v", state.CancelledAt)
	}
}

func TestRedisJobStore_RehydrateAfterInstanceSwap(t *testing.T) {
	// Integration-style: simulate app restart. First RedisJobStore writes
	// state; a second RedisJobStore (same Redis, same prefix) sees it —
	// exactly what app.Run does after a crash.
	client := testRedisClient(t)
	first := NewRedisJobStore(client, "jobstore", time.Minute)
	if err := first.Put(&Job{ID: "survivor", ChannelID: "C1", ThreadTS: "T1", SubmittedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := first.UpdateStatus("survivor", JobRunning); err != nil {
		t.Fatal(err)
	}

	// "Old app" drops its MemJobStore equivalent — here just stop using
	// `first`. New process starts:
	second := NewRedisJobStore(client, "jobstore", time.Minute)
	state, err := second.Get("survivor")
	if err != nil {
		t.Fatalf("new instance cannot see survivor: %v", err)
	}
	if state.Status != JobRunning {
		t.Errorf("rehydrated status = %q, want running", state.Status)
	}
	all, err := second.ListAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || all[0].Job.ID != "survivor" {
		t.Errorf("ListAll on new instance: %+v", all)
	}
}
