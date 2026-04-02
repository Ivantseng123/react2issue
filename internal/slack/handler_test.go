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
