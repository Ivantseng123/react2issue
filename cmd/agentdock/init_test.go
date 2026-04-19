package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInitApp_YAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.yaml")
	if err := runInitApp(path, false, false); err != nil {
		t.Fatalf("runInitApp: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if !strings.Contains(content, "# REQUIRED") {
		t.Error("app.yaml output should contain # REQUIRED comments")
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0o600 {
		t.Errorf("file mode = %o, want 0600", info.Mode().Perm())
	}
}

func TestInitApp_JSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.json")
	if err := runInitApp(path, false, false); err != nil {
		t.Fatalf("runInitApp: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	if strings.Contains(string(data), "# REQUIRED") {
		t.Error("JSON output should NOT contain comments")
	}
}

func TestInitApp_RejectsExisting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.yaml")
	if err := os.WriteFile(path, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := runInitApp(path, false, false)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("expected 'already exists' error, got %v", err)
	}
}

func TestInitApp_ForceOverwrites(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.yaml")
	if err := os.WriteFile(path, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runInitApp(path, false, true); err != nil {
		t.Fatalf("runInitApp force: %v", err)
	}
	data, _ := os.ReadFile(path)
	if string(data) == "existing" {
		t.Error("existing content should have been overwritten")
	}
}

func TestInitApp_InteractiveRejectsNonTTY(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.yaml")
	err := runInitApp(path, true, false)
	if err == nil {
		t.Fatal("expected error for interactive mode without TTY")
	}
	if !strings.Contains(err.Error(), "requires a terminal") {
		t.Errorf("expected 'requires a terminal' error, got: %v", err)
	}
	if _, statErr := os.Stat(path); statErr == nil {
		t.Error("config file should not exist after TTY rejection")
	}
}

func TestInitWorker_YAML_IncludesAgents(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "worker.yaml")
	if err := runInitWorker(path, false, false); err != nil {
		t.Fatalf("runInitWorker: %v", err)
	}
	data, _ := os.ReadFile(path)
	content := string(data)
	for _, name := range []string{"claude:", "codex:", "opencode:"} {
		if !strings.Contains(content, name) {
			t.Errorf("worker.yaml missing agent %q block", name)
		}
	}
	if !strings.Contains(content, "# REQUIRED") {
		t.Error("worker.yaml should include # REQUIRED hints")
	}
}

func TestInitWorker_JSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "worker.json")
	if err := runInitWorker(path, false, false); err != nil {
		t.Fatalf("runInitWorker: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	if strings.Contains(string(data), "# REQUIRED") {
		t.Error("JSON output should NOT contain comments")
	}
}
