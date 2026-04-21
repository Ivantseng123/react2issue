package prreview

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestFingerprintLocal_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	fp, err := fingerprintLocal(dir)
	if err != nil {
		t.Fatalf("fingerprintLocal: %v", err)
	}
	if fp.Language != "" {
		t.Errorf("empty dir: want language=\"\", got %q", fp.Language)
	}
	if fp.Confidence != "low" {
		t.Errorf("empty dir: want confidence=low, got %q", fp.Confidence)
	}
	if len(fp.StyleSources) != 0 {
		t.Errorf("empty dir: want no style_sources, got %v", fp.StyleSources)
	}
}

func TestFingerprintLocal_GoModHigh(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "go.mod"), "module x\n\ngo 1.25\n")
	mustWrite(t, filepath.Join(dir, "main.go"), "package main\n")
	mustWrite(t, filepath.Join(dir, "lib.go"), "package x\n")

	fp, err := fingerprintLocal(dir)
	if err != nil {
		t.Fatalf("fingerprintLocal: %v", err)
	}
	if fp.Language != "go" {
		t.Errorf("language: want go, got %q", fp.Language)
	}
	if fp.Confidence != "high" {
		t.Errorf("confidence: want high, got %q", fp.Confidence)
	}
	if fp.TestRunner != "go test" {
		t.Errorf("test_runner: want 'go test', got %q", fp.TestRunner)
	}
}

func TestFingerprintLocal_TSOverrideJS(t *testing.T) {
	dir := t.TempDir()
	pkg := `{"name":"x","dependencies":{"typescript":"^5.0.0"}}`
	mustWrite(t, filepath.Join(dir, "package.json"), pkg)
	mustWrite(t, filepath.Join(dir, "index.ts"), "export {}")

	fp, err := fingerprintLocal(dir)
	if err != nil {
		t.Fatalf("fingerprintLocal: %v", err)
	}
	if fp.Language != "ts" {
		t.Errorf("language: want ts, got %q", fp.Language)
	}
}

func TestFingerprintLocal_StyleSources(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "go.mod"), "module x\n\ngo 1.25\n")
	mustWrite(t, filepath.Join(dir, "CLAUDE.md"), "# rules")
	mustWrite(t, filepath.Join(dir, ".golangci.yml"), "linters: []\n")

	fp, err := fingerprintLocal(dir)
	if err != nil {
		t.Fatalf("fingerprintLocal: %v", err)
	}
	if !containsString(fp.StyleSources, "CLAUDE.md") {
		t.Errorf("want CLAUDE.md in style_sources, got %v", fp.StyleSources)
	}
	if !containsString(fp.StyleSources, ".golangci.yml") {
		t.Errorf("want .golangci.yml in style_sources, got %v", fp.StyleSources)
	}
}

func TestFingerprintLocal_FrameworkNext(t *testing.T) {
	dir := t.TempDir()
	pkg := `{"dependencies":{"next":"^14.0.0","react":"^18.0.0"}}`
	mustWrite(t, filepath.Join(dir, "package.json"), pkg)
	fp, err := fingerprintLocal(dir)
	if err != nil {
		t.Fatalf("fingerprintLocal: %v", err)
	}
	if fp.Framework != "next" {
		t.Errorf("framework: want next, got %q", fp.Framework)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func containsString(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

var _ = context.Background
