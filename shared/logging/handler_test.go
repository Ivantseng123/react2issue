package logging

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

func TestMultiHandler_WritesToBoth(t *testing.T) {
	var stderrBuf, fileBuf bytes.Buffer
	h := NewMultiHandler(
		slog.NewTextHandler(&stderrBuf, &slog.HandlerOptions{Level: slog.LevelInfo}),
		slog.NewJSONHandler(&fileBuf, &slog.HandlerOptions{Level: slog.LevelDebug}),
	)
	logger := slog.New(h)

	logger.Info("test message", "key", "value")

	if !strings.Contains(stderrBuf.String(), "test message") {
		t.Error("stderr missing log entry")
	}

	var entry map[string]any
	if err := json.Unmarshal(fileBuf.Bytes(), &entry); err != nil {
		t.Fatalf("file output not valid JSON: %v", err)
	}
	if entry["msg"] != "test message" {
		t.Errorf("file msg = %v, want %q", entry["msg"], "test message")
	}
}

func TestMultiHandler_IndependentLevels(t *testing.T) {
	var stderrBuf, fileBuf bytes.Buffer
	h := NewMultiHandler(
		slog.NewTextHandler(&stderrBuf, &slog.HandlerOptions{Level: slog.LevelWarn}),
		slog.NewJSONHandler(&fileBuf, &slog.HandlerOptions{Level: slog.LevelDebug}),
	)
	logger := slog.New(h)

	logger.Info("info only")

	if stderrBuf.Len() > 0 {
		t.Error("stderr should not have INFO when level is WARN")
	}
	if !strings.Contains(fileBuf.String(), "info only") {
		t.Error("file should have INFO when level is DEBUG")
	}
}

func TestMultiHandler_WithAttrs(t *testing.T) {
	var fileBuf bytes.Buffer
	h := NewMultiHandler(
		slog.NewTextHandler(&bytes.Buffer{}, &slog.HandlerOptions{Level: slog.LevelInfo}),
		slog.NewJSONHandler(&fileBuf, &slog.HandlerOptions{Level: slog.LevelDebug}),
	)
	logger := slog.New(h).With("request_id", "abc123")

	logger.Info("with attr")

	var entry map[string]any
	json.Unmarshal(fileBuf.Bytes(), &entry)
	if entry["request_id"] != "abc123" {
		t.Errorf("request_id = %v, want %q", entry["request_id"], "abc123")
	}
}
