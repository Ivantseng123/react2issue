package diagnosis

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"slack-issue-bot/internal/llm"
)

// initGitRepo creates a temp git repo with given files.
func initGitRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init")
	runGit(t, dir, "config", "user.name", "test")
	runGit(t, dir, "config", "user.email", "test@test.com")
	for path, content := range files {
		full := filepath.Join(dir, path)
		os.MkdirAll(filepath.Dir(full), 0755)
		os.WriteFile(full, []byte(content), 0644)
	}
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "init")
	return dir
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
}

// --- Step 1 test: keyword grep hits directly ---

func TestEngine_Diagnose_DirectGrepHit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}

	repoDir := initGitRepo(t, map[string]string{
		"src/login.go": "package auth\n\nfunc Login() {}",
	})

	mock := &sequentialMock{responses: []llm.DiagnoseResponse{
		{Summary: "login bug", Files: []llm.FileRef{{Path: "src/login.go"}}},
	}}

	engine := NewEngine(mock, 5)
	resp, err := engine.Diagnose(context.Background(), DiagnoseInput{
		Type:     "bug",
		Message:  "login crashes",
		RepoPath: repoDir,
		Keywords: []string{"Login"},
	})
	if err != nil {
		t.Fatalf("Diagnose failed: %v", err)
	}
	if mock.calls != 1 {
		t.Errorf("expected 1 LLM call (direct grep hit), got %d", mock.calls)
	}
	if resp.Summary != "login bug" {
		t.Errorf("unexpected summary: %s", resp.Summary)
	}
}

// --- Step 2 test: LLM suggests search terms ---

func TestEngine_Diagnose_LLMSuggestsSearchTerms(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}

	repoDir := initGitRepo(t, map[string]string{
		"src/reinsurance/cession.go": "package reinsurance\n\nfunc CalcCession() {}",
		"src/models/policy.go":       "package models\n\ntype Policy struct{}",
	})

	mock := &sequentialMock{responses: []llm.DiagnoseResponse{
		// Turn 1: suggest search terms
		{Summary: `["reinsurance", "cession", "CalcCession"]`},
		// Turn 2: final diagnosis (grep hit with suggested terms)
		{Summary: "分保功能在 cession.go", Files: []llm.FileRef{{Path: "src/reinsurance/cession.go"}}},
	}}

	engine := NewEngine(mock, 5)
	resp, err := engine.Diagnose(context.Background(), DiagnoseInput{
		Type:     "feature",
		Message:  "再保系統分保結果畫面，新增出單單位欄位",
		RepoPath: repoDir,
		Keywords: []string{"再保系統", "分保結果"},
	})
	if err != nil {
		t.Fatalf("Diagnose failed: %v", err)
	}
	if mock.calls != 2 {
		t.Errorf("expected 2 LLM calls (suggest terms + diagnose), got %d", mock.calls)
	}
	if resp.Summary != "分保功能在 cession.go" {
		t.Errorf("unexpected summary: %s", resp.Summary)
	}
}

// --- Step 3 test: all grep fails, LLM picks from tree ---

func TestEngine_Diagnose_LLMPicksFromTree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}

	repoDir := initGitRepo(t, map[string]string{
		"src/module_a/handler.go": "package module_a\n\nfunc Handle() {}",
		"src/module_b/service.go": "package module_b\n\nfunc Serve() {}",
	})

	mock := &sequentialMock{responses: []llm.DiagnoseResponse{
		// Turn 1: suggest search terms (won't match anything)
		{Summary: `["xyz_nonexistent"]`},
		// Turn 2: pick files from tree
		{Summary: `["src/module_a/handler.go"]`},
		// Turn 3: final diagnosis
		{Summary: "found it in handler", Files: []llm.FileRef{{Path: "src/module_a/handler.go"}}},
	}}

	engine := NewEngine(mock, 5)
	resp, err := engine.Diagnose(context.Background(), DiagnoseInput{
		Type:     "bug",
		Message:  "某個完全無法 grep 到的描述",
		RepoPath: repoDir,
		Keywords: []string{"完全不會命中"},
	})
	if err != nil {
		t.Fatalf("Diagnose failed: %v", err)
	}
	if mock.calls != 3 {
		t.Errorf("expected 3 LLM calls (suggest + pick + diagnose), got %d", mock.calls)
	}
	if resp.Summary != "found it in handler" {
		t.Errorf("unexpected summary: %s", resp.Summary)
	}
}

// --- parseStringArray tests ---

func TestParseStringArray(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  int
	}{
		{"plain array", `["a.go", "b.go"]`, 2},
		{"with code block", "```json\n[\"a.go\"]\n```", 1},
		{"embedded in text", "Here are the terms: [\"reinsurance\", \"cession\"] for searching", 2},
		{"invalid", "no json here", 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			arr, err := parseStringArray(tt.input)
			if tt.want == 0 {
				if err == nil {
					t.Error("expected error for invalid input")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(arr) != tt.want {
				t.Errorf("expected %d items, got %d: %v", tt.want, len(arr), arr)
			}
		})
	}
}

// --- Mock ---

// sequentialMock returns different responses for each successive call.
type sequentialMock struct {
	calls     int
	responses []llm.DiagnoseResponse
	lastReq   llm.DiagnoseRequest
}

func (m *sequentialMock) Name() string { return "sequential-mock" }
func (m *sequentialMock) Diagnose(ctx context.Context, req llm.DiagnoseRequest) (llm.DiagnoseResponse, error) {
	m.lastReq = req
	idx := m.calls
	m.calls++
	if idx < len(m.responses) {
		return m.responses[idx], nil
	}
	return llm.DiagnoseResponse{Summary: "fallback"}, nil
}
