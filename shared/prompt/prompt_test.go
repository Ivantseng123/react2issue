package prompt

import (
	"bytes"
	"strings"
	"testing"
)

func TestOK_PrintsGreenCheckmark(t *testing.T) {
	var buf bytes.Buffer
	origStderr := Stderr
	Stderr = &buf
	defer func() { Stderr = origStderr }()

	OK("hello %s", "world")

	out := buf.String()
	if !strings.Contains(out, "hello world") {
		t.Errorf("output missing message: %q", out)
	}
	if !strings.Contains(out, "\033[32m") {
		t.Errorf("output missing green color: %q", out)
	}
}

func TestFail_PrintsRedCross(t *testing.T) {
	var buf bytes.Buffer
	origStderr := Stderr
	Stderr = &buf
	defer func() { Stderr = origStderr }()

	Fail("bad: %d", 42)

	out := buf.String()
	if !strings.Contains(out, "bad: 42") {
		t.Errorf("output missing message: %q", out)
	}
	if !strings.Contains(out, "\033[31m") {
		t.Errorf("output missing red color: %q", out)
	}
}

func TestCheckAgentCLI_MissingBinaryReturnsError(t *testing.T) {
	_, err := CheckAgentCLI("definitely-not-a-real-binary-12345")
	if err == nil {
		t.Error("expected error for missing binary")
	}
}
