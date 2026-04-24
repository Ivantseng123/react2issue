package agent

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Ivantseng123/agentdock/worker/config"
)

func TestRunner_Success(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "mock-agent")
	os.WriteFile(script, []byte(`#!/bin/sh
echo "## Summary"
echo ""
echo "Test issue body"
echo ""
echo "===TRIAGE_METADATA==="
echo '{"issue_type":"bug","confidence":"high","files":[],"open_questions":[],"suggested_title":"test"}'
`), 0755)

	runner := NewRunner([]config.AgentConfig{
		{Command: script, Args: []string{"{prompt}"}, Timeout: 10 * time.Second},
	})

	output, err := runner.Run(context.Background(), slog.Default(), dir, "test prompt", RunOptions{})
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if output == "" {
		t.Error("output is empty")
	}
	if !strings.Contains(output, "Test issue body") {
		t.Errorf("output missing expected content: %q", output)
	}
}

func TestRunner_ProviderChain(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "good-agent")
	os.WriteFile(script, []byte("#!/bin/sh\necho 'fallback output with enough characters to pass the minimum length check of fifty chars'\n"), 0755)

	runner := NewRunner([]config.AgentConfig{
		{Command: "/nonexistent/agent", Args: []string{"{prompt}"}, Timeout: 5 * time.Second},
		{Command: script, Args: []string{"{prompt}"}, Timeout: 5 * time.Second},
	})

	output, err := runner.Run(context.Background(), slog.Default(), dir, "test", RunOptions{})
	if err != nil {
		t.Fatalf("Run with provider chain failed: %v", err)
	}
	if !strings.Contains(output, "fallback output") {
		t.Errorf("output = %q, want fallback output", output)
	}
}

func TestRunner_AllFail(t *testing.T) {
	runner := NewRunner([]config.AgentConfig{
		{Command: "/nonexistent/agent1", Args: []string{"{prompt}"}, Timeout: 5 * time.Second},
		{Command: "/nonexistent/agent2", Args: []string{"{prompt}"}, Timeout: 5 * time.Second},
	})

	_, err := runner.Run(context.Background(), slog.Default(), t.TempDir(), "test", RunOptions{})
	if err == nil {
		t.Error("expected error when all agents fail")
	}
}

func TestRunner_Timeout(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "slow-agent")
	os.WriteFile(script, []byte("#!/bin/sh\nsleep 10\n"), 0755)

	runner := NewRunner([]config.AgentConfig{
		{Command: script, Args: []string{"{prompt}"}, Timeout: 100 * time.Millisecond},
	})

	_, err := runner.Run(context.Background(), slog.Default(), dir, "test", RunOptions{})
	if err == nil {
		t.Error("expected timeout error")
	}
}

func TestRunner_PromptSubstitution(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "echo-agent")
	os.WriteFile(script, []byte(`#!/bin/sh
echo "$1"
echo "padding padding padding padding padding padding padding"
`), 0755)

	runner := NewRunner([]config.AgentConfig{
		{Command: script, Args: []string{"{prompt}"}, Timeout: 5 * time.Second},
	})

	output, err := runner.Run(context.Background(), slog.Default(), dir, "hello world", RunOptions{})
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if !strings.Contains(output, "hello world") {
		t.Errorf("prompt not substituted in output: %q", output)
	}
}

func TestRunner_SecretsInjected(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "env-agent")
	os.WriteFile(script, []byte(`#!/bin/sh
env | grep TOKEN | sort
echo "padding padding padding padding padding padding padding"
`), 0755)

	// RequiredSecrets explicitly lists both keys so both are forwarded.
	runner := NewRunner([]config.AgentConfig{
		{
			Command:         script,
			Args:            []string{"{prompt}"},
			Timeout:         5 * time.Second,
			RequiredSecrets: []string{"GH_TOKEN", "K8S_TOKEN"},
		},
	})

	secrets := map[string]string{
		"GH_TOKEN":  "ghp_from_secrets",
		"K8S_TOKEN": "k8s_val",
	}
	output, err := runner.Run(context.Background(), slog.Default(), dir, "test", RunOptions{Secrets: secrets})
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if !strings.Contains(output, "GH_TOKEN=ghp_from_secrets") {
		t.Errorf("GH_TOKEN not injected: %q", output)
	}
	if !strings.Contains(output, "K8S_TOKEN=k8s_val") {
		t.Errorf("K8S_TOKEN not injected: %q", output)
	}
}

// TestRunner_SecretWhitelist_FiltersToRequired verifies that when RequiredSecrets
// names only one key, the child env receives only that key, not the others.
func TestRunner_SecretWhitelist_FiltersToRequired(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "env-agent")
	os.WriteFile(script, []byte(`#!/bin/sh
env | grep -E 'GH_TOKEN|K8S_TOKEN|JIRA_TOKEN' | sort
echo "padding padding padding padding padding padding padding"
`), 0755)

	// Provider only needs GH_TOKEN.
	runner := NewRunner([]config.AgentConfig{
		{
			Command:         script,
			Args:            []string{"{prompt}"},
			Timeout:         5 * time.Second,
			RequiredSecrets: []string{"GH_TOKEN"},
		},
	})

	secrets := map[string]string{
		"GH_TOKEN":   "ghp_only",
		"K8S_TOKEN":  "k8s_secret",
		"JIRA_TOKEN": "jira_secret",
	}
	output, err := runner.Run(context.Background(), slog.Default(), dir, "test", RunOptions{Secrets: secrets})
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if !strings.Contains(output, "GH_TOKEN=ghp_only") {
		t.Errorf("GH_TOKEN should be injected: %q", output)
	}
	if strings.Contains(output, "K8S_TOKEN") {
		t.Errorf("K8S_TOKEN must not be injected (not in whitelist): %q", output)
	}
	if strings.Contains(output, "JIRA_TOKEN") {
		t.Errorf("JIRA_TOKEN must not be injected (not in whitelist): %q", output)
	}
}

// TestRunner_SecretWhitelist_EmptyListMeansZeroSecrets verifies zero-trust
// provider: required_secrets: [] forwards no secrets at all.
func TestRunner_SecretWhitelist_EmptyListMeansZeroSecrets(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "env-agent")
	os.WriteFile(script, []byte(`#!/bin/sh
env | grep -E 'GH_TOKEN|K8S_TOKEN' | sort
echo "padding padding padding padding padding padding padding"
`), 0755)

	runner := NewRunner([]config.AgentConfig{
		{
			Command:         script,
			Args:            []string{"{prompt}"},
			Timeout:         5 * time.Second,
			RequiredSecrets: []string{}, // explicit empty → zero secrets
		},
	})

	secrets := map[string]string{
		"GH_TOKEN":  "ghp_should_not_appear",
		"K8S_TOKEN": "k8s_should_not_appear",
	}
	output, err := runner.Run(context.Background(), slog.Default(), dir, "test", RunOptions{Secrets: secrets})
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if strings.Contains(output, "GH_TOKEN") {
		t.Errorf("GH_TOKEN must not be injected for zero-trust provider: %q", output)
	}
	if strings.Contains(output, "K8S_TOKEN") {
		t.Errorf("K8S_TOKEN must not be injected for zero-trust provider: %q", output)
	}
}

// TestRunner_SecretWhitelist_NilFallsBackToGHToken verifies that a provider
// with RequiredSecrets == nil (not declared in yaml) defaults to GH_TOKEN only
// for back-compat, and does NOT forward other secrets.
func TestRunner_SecretWhitelist_NilFallsBackToGHToken(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "env-agent")
	os.WriteFile(script, []byte(`#!/bin/sh
env | grep -E 'GH_TOKEN|K8S_TOKEN' | sort
echo "padding padding padding padding padding padding padding"
`), 0755)

	// RequiredSecrets is nil (not set).
	runner := NewRunner([]config.AgentConfig{
		{
			Command: script,
			Args:    []string{"{prompt}"},
			Timeout: 5 * time.Second,
			// RequiredSecrets intentionally absent (nil)
		},
	})

	secrets := map[string]string{
		"GH_TOKEN":  "ghp_backcompat",
		"K8S_TOKEN": "k8s_should_not_appear",
	}
	output, err := runner.Run(context.Background(), slog.Default(), dir, "test", RunOptions{Secrets: secrets})
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if !strings.Contains(output, "GH_TOKEN=ghp_backcompat") {
		t.Errorf("GH_TOKEN should be injected for nil whitelist (back-compat): %q", output)
	}
	if strings.Contains(output, "K8S_TOKEN") {
		t.Errorf("K8S_TOKEN must not be injected when whitelist is nil (default=GH_TOKEN only): %q", output)
	}
}

func TestRunner_GithubTokenFallback(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "env-agent")
	os.WriteFile(script, []byte(`#!/bin/sh
env | grep GH_TOKEN
echo "padding padding padding padding padding padding padding"
`), 0755)

	runner := NewRunner([]config.AgentConfig{
		{Command: script, Args: []string{"{prompt}"}, Timeout: 5 * time.Second},
	})
	runner.githubToken = "ghp_fallback"

	output, err := runner.Run(context.Background(), slog.Default(), dir, "test", RunOptions{})
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if !strings.Contains(output, "GH_TOKEN=ghp_fallback") {
		t.Errorf("githubToken fallback not working: %q", output)
	}
}

func TestRunner_OutputFilePlaceholder(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "outfile-agent")
	// Script writes clean text to $2 (the -o path) and prints noise to stdout.
	// Runner must return the file content, not the stdout noise.
	os.WriteFile(script, []byte(`#!/bin/sh
echo "noisy stdout header"
printf '%s' "clean answer from file" > "$2"
echo "noisy stdout footer"
`), 0755)

	runner := NewRunner([]config.AgentConfig{
		{Command: script, Args: []string{"-o", "{output_file}", "{prompt}"}, Timeout: 5 * time.Second},
	})

	output, err := runner.Run(context.Background(), slog.Default(), dir, "test prompt", RunOptions{})
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if output != "clean answer from file" {
		t.Errorf("expected file content, got: %q", output)
	}
	if strings.Contains(output, "noisy") {
		t.Errorf("output must not contain stdout noise, got: %q", output)
	}
}

func TestRunner_OutputFileCleanedUp(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "outfile-agent")
	os.WriteFile(script, []byte(`#!/bin/sh
printf 'x' > "$2"
echo "$2" > `+filepath.Join(dir, "captured-path.txt")+`
`), 0755)

	runner := NewRunner([]config.AgentConfig{
		{Command: script, Args: []string{"-o", "{output_file}", "{prompt}"}, Timeout: 5 * time.Second},
	})

	_, err := runner.Run(context.Background(), slog.Default(), dir, "p", RunOptions{})
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	captured, err := os.ReadFile(filepath.Join(dir, "captured-path.txt"))
	if err != nil {
		t.Fatalf("read captured path: %v", err)
	}
	tmpPath := strings.TrimSpace(string(captured))
	if _, statErr := os.Stat(tmpPath); !os.IsNotExist(statErr) {
		t.Errorf("temp output file %q should have been removed, statErr=%v", tmpPath, statErr)
	}
}

func TestNewRunnerFromConfig_EmptyProviders(t *testing.T) {
	cfg := &config.Config{
		Agents:    map[string]config.AgentConfig{},
		Providers: []string{},
	}
	runner := NewRunnerFromConfig(cfg)
	_, err := runner.Run(context.Background(), slog.Default(), t.TempDir(), "test", RunOptions{})
	if err == nil {
		t.Error("expected error when providers is empty")
	}
}

func TestNewRunnerFromConfig_SingleProvider(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "single-agent")
	os.WriteFile(script, []byte("#!/bin/sh\necho 'output from single provider agent padding padding padding'\n"), 0755)

	cfg := &config.Config{
		Agents: map[string]config.AgentConfig{
			"myagent": {Command: script, Args: []string{"{prompt}"}, Timeout: 5 * time.Second},
		},
		Providers: []string{"myagent"},
	}
	runner := NewRunnerFromConfig(cfg)
	output, err := runner.Run(context.Background(), slog.Default(), dir, "test", RunOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(output, "single provider") {
		t.Errorf("unexpected output: %q", output)
	}
}

// ── filterSecrets unit tests ─────────────────────────────────────────────────

func TestFilterSecrets_NilWhitelistDefaultsToGHToken(t *testing.T) {
	all := map[string]string{"GH_TOKEN": "ghp_x", "K8S": "k8s_x"}
	got := filterSecrets(all, nil)
	if got["GH_TOKEN"] != "ghp_x" {
		t.Errorf("GH_TOKEN = %q, want %q", got["GH_TOKEN"], "ghp_x")
	}
	if _, ok := got["K8S"]; ok {
		t.Error("K8S must not appear when whitelist is nil (defaults to GH_TOKEN only)")
	}
}

func TestFilterSecrets_EmptyWhitelistReturnsEmpty(t *testing.T) {
	all := map[string]string{"GH_TOKEN": "ghp_x", "K8S": "k8s_x"}
	got := filterSecrets(all, []string{})
	if len(got) != 0 {
		t.Errorf("expected empty map, got %v", got)
	}
}

func TestFilterSecrets_ExplicitWhitelistFilters(t *testing.T) {
	all := map[string]string{
		"GH_TOKEN":   "ghp_x",
		"K8S_TOKEN":  "k8s_x",
		"JIRA_TOKEN": "jira_x",
	}
	got := filterSecrets(all, []string{"GH_TOKEN", "JIRA_TOKEN"})
	if got["GH_TOKEN"] != "ghp_x" {
		t.Errorf("GH_TOKEN = %q", got["GH_TOKEN"])
	}
	if got["JIRA_TOKEN"] != "jira_x" {
		t.Errorf("JIRA_TOKEN = %q", got["JIRA_TOKEN"])
	}
	if _, ok := got["K8S_TOKEN"]; ok {
		t.Error("K8S_TOKEN must not appear — not in whitelist")
	}
}

func TestFilterSecrets_MissingKeyInAllIsSkipped(t *testing.T) {
	all := map[string]string{"GH_TOKEN": "ghp_x"}
	got := filterSecrets(all, []string{"GH_TOKEN", "ABSENT_KEY"})
	if got["GH_TOKEN"] != "ghp_x" {
		t.Errorf("GH_TOKEN = %q", got["GH_TOKEN"])
	}
	if _, ok := got["ABSENT_KEY"]; ok {
		t.Error("ABSENT_KEY must not appear when it's missing from all")
	}
}

func TestRunner_CancelShortCircuitsProviderChain(t *testing.T) {
	runner := &Runner{
		agents: []config.AgentConfig{
			{Command: "nonexistent-agent-one", Args: []string{"{prompt}"}, Timeout: time.Second},
			{Command: "nonexistent-agent-two", Args: []string{"{prompt}"}, Timeout: time.Second},
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := runner.Run(ctx, slog.Default(), t.TempDir(), "noop", RunOptions{})
	if err == nil {
		t.Fatal("expected error on cancelled ctx")
	}
	if err.Error() != "cancelled" {
		t.Errorf("err = %q, want \"cancelled\" (chain must not try the second agent)", err.Error())
	}
}
