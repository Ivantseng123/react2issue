package logging

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSaveAgentOutput(t *testing.T) {
	dir := t.TempDir()
	path, err := SaveAgentOutput(dir, "20260410-143052-a3f8", "org/backend", "## Issue\n\nSome content")
	if err != nil {
		t.Fatal(err)
	}

	if !strings.HasSuffix(path, "20260410-143052-a3f8.md") {
		t.Errorf("path = %q, want suffix 20260410-143052-a3f8.md", path)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "## Issue\n\nSome content" {
		t.Errorf("content = %q", string(data))
	}
}

func TestSaveAgentOutput_CreatesDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "dir")
	_, err := SaveAgentOutput(dir, "test-id", "org/repo", "content")
	if err != nil {
		t.Fatalf("should create nested dirs: %v", err)
	}
}
