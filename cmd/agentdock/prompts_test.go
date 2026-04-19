package main

import (
	"testing"

	"github.com/Ivantseng123/agentdock/shared/connectivity"
)

func TestCheckSlackToken_RejectsEmpty(t *testing.T) {
	if _, err := connectivity.CheckSlackToken(""); err == nil {
		t.Error("expected error for empty token")
	}
}

func TestCheckSlackToken_RejectsBadPrefix(t *testing.T) {
	if _, err := connectivity.CheckSlackToken("not-a-slack-token"); err == nil {
		t.Error("expected error for token without xoxb- prefix")
	}
}
