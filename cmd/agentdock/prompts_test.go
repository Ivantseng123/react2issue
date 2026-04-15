package main

import "testing"

func TestCheckSlackToken_RejectsEmpty(t *testing.T) {
	if _, err := checkSlackToken(""); err == nil {
		t.Error("expected error for empty token")
	}
}

func TestCheckSlackToken_RejectsBadPrefix(t *testing.T) {
	if _, err := checkSlackToken("not-a-slack-token"); err == nil {
		t.Error("expected error for token without xoxb- prefix")
	}
}
