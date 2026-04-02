package slack

import (
	"testing"
	"time"
)

func TestDedup_FirstEventPasses(t *testing.T) {
	d := newDedup(5 * time.Minute)
	if d.isDuplicate("evt-1") {
		t.Error("first event should not be duplicate")
	}
}

func TestDedup_SecondEventBlocked(t *testing.T) {
	d := newDedup(5 * time.Minute)
	d.isDuplicate("evt-1")
	if !d.isDuplicate("evt-1") {
		t.Error("second event with same ID should be duplicate")
	}
}

func TestDedup_ExpiredEventPasses(t *testing.T) {
	d := newDedup(1 * time.Millisecond)
	d.isDuplicate("evt-1")
	time.Sleep(5 * time.Millisecond)
	if d.isDuplicate("evt-1") {
		t.Error("expired event should not be duplicate")
	}
}

func TestDedup_DifferentEventsPasses(t *testing.T) {
	d := newDedup(5 * time.Minute)
	d.isDuplicate("evt-1")
	if d.isDuplicate("evt-2") {
		t.Error("different event ID should not be duplicate")
	}
}

func TestRateLimiter_AllowsWithinLimit(t *testing.T) {
	rl := newRateLimiter(3, time.Minute)
	for i := 0; i < 3; i++ {
		if !rl.allow("user1") {
			t.Errorf("request %d should be allowed", i+1)
		}
	}
}

func TestRateLimiter_BlocksOverLimit(t *testing.T) {
	rl := newRateLimiter(2, time.Minute)
	rl.allow("user1")
	rl.allow("user1")
	if rl.allow("user1") {
		t.Error("third request should be blocked")
	}
}

func TestRateLimiter_ResetsAfterWindow(t *testing.T) {
	rl := newRateLimiter(1, 1*time.Millisecond)
	rl.allow("user1")
	time.Sleep(5 * time.Millisecond)
	if !rl.allow("user1") {
		t.Error("should allow after window expires")
	}
}

func TestRateLimiter_NilDisabled(t *testing.T) {
	rl := newRateLimiter(0, 0)
	if rl != nil {
		t.Error("expected nil for disabled rate limiter")
	}
	// nil rate limiter should always allow
	var nilRL *rateLimiter
	if !nilRL.allow("anything") {
		t.Error("nil rate limiter should always allow")
	}
}

func TestRateLimiter_IndependentKeys(t *testing.T) {
	rl := newRateLimiter(1, time.Minute)
	rl.allow("user1")
	if !rl.allow("user2") {
		t.Error("user2 should not be affected by user1's limit")
	}
}

func TestMessageDedup_SameMessageBlocked(t *testing.T) {
	d := newMessageDedup(5 * time.Minute)
	if d.isDuplicate("C1", "ts1") {
		t.Error("first should pass")
	}
	if !d.isDuplicate("C1", "ts1") {
		t.Error("same message should be blocked")
	}
}

func TestMessageDedup_DifferentMessageAllowed(t *testing.T) {
	d := newMessageDedup(5 * time.Minute)
	d.isDuplicate("C1", "ts1")
	if d.isDuplicate("C1", "ts2") {
		t.Error("different message should pass")
	}
}
