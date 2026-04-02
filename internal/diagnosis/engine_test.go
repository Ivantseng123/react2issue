package diagnosis

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"slack-issue-bot/internal/llm"
)

type mockLLM struct {
	resp  llm.DiagnoseResponse
	err   error
	got   llm.DiagnoseRequest
	calls int
}

func (m *mockLLM) Name() string { return "mock" }
func (m *mockLLM) Diagnose(ctx context.Context, req llm.DiagnoseRequest) (llm.DiagnoseResponse, error) {
	m.calls++
	m.got = req
	return m.resp, m.err
}

// initGitRepo creates a temp git repo with given files so git grep works.
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

func TestEngine_Diagnose_KeywordMatch(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}

	repoDir := initGitRepo(t, map[string]string{
		"src/login.go": "package auth\n\nfunc Login() {}",
		"src/user.go":  "package auth\n\ntype User struct{}",
	})

	mock := &mockLLM{
		resp: llm.DiagnoseResponse{
			Summary:     "login function missing validation",
			Files:       []llm.FileRef{{Path: "src/login.go", LineNumber: 3, Description: "Login func"}},
			Suggestions: []string{"Add input validation"},
		},
	}

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
	if resp.Summary != "login function missing validation" {
		t.Errorf("unexpected summary: %s", resp.Summary)
	}
	// Keyword grep should find login.go, so only 1 LLM call (the diagnosis)
	if mock.calls != 1 {
		t.Errorf("expected 1 LLM call (keyword matched), got %d", mock.calls)
	}
	if len(mock.got.RepoFiles) == 0 {
		t.Error("expected repo files to be passed to LLM")
	}
}

func TestEngine_Diagnose_TwoPass_NoKeywordMatch(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}

	repoDir := initGitRepo(t, map[string]string{
		"src/reinsurance/cession.go": "package reinsurance\n\nfunc CalcCession() {}",
		"src/reinsurance/result.go":  "package reinsurance\n\nfunc ShowResult() {}",
		"src/models/policy.go":       "package models\n\ntype Policy struct{}",
	})

	callCount := 0
	mock := &mockLLM{}
	// Override Diagnose to return different responses per call
	origDiagnose := mock.Diagnose
	_ = origDiagnose

	// We need a smarter mock that returns file list on pass 1, diagnosis on pass 2
	twoPassMock := &twoPassLLM{
		pass1Resp: llm.DiagnoseResponse{
			Summary: `["src/reinsurance/cession.go", "src/reinsurance/result.go"]`,
		},
		pass2Resp: llm.DiagnoseResponse{
			Summary:     "分保結果功能在 cession.go",
			Files:       []llm.FileRef{{Path: "src/reinsurance/result.go", LineNumber: 3, Description: "ShowResult"}},
			Suggestions: []string{"在 result.go 加入通訊處欄位"},
		},
	}

	engine := NewEngine(twoPassMock, 5)
	resp, err := engine.Diagnose(context.Background(), DiagnoseInput{
		Type:     "feature",
		Message:  "再保系統分保結果畫面，新增出單單位欄位",
		RepoPath: repoDir,
		Keywords: []string{"再保系統", "分保結果", "出單單位"}, // won't match any code
	})
	_ = callCount
	if err != nil {
		t.Fatalf("Diagnose failed: %v", err)
	}
	if twoPassMock.calls != 2 {
		t.Errorf("expected 2 LLM calls (two-pass), got %d", twoPassMock.calls)
	}
	if resp.Summary != "分保結果功能在 cession.go" {
		t.Errorf("unexpected summary: %s", resp.Summary)
	}
	// Pass 2 should have received actual file contents
	if len(twoPassMock.pass2Got.RepoFiles) == 0 {
		t.Error("expected repo files in pass 2")
	}
}

// twoPassLLM returns different responses for pass 1 (file picker) and pass 2 (diagnosis).
type twoPassLLM struct {
	calls    int
	pass1Resp llm.DiagnoseResponse
	pass2Resp llm.DiagnoseResponse
	pass2Got  llm.DiagnoseRequest
}

func (m *twoPassLLM) Name() string { return "two-pass-mock" }
func (m *twoPassLLM) Diagnose(ctx context.Context, req llm.DiagnoseRequest) (llm.DiagnoseResponse, error) {
	m.calls++
	if m.calls == 1 {
		return m.pass1Resp, nil
	}
	m.pass2Got = req
	return m.pass2Resp, nil
}

func TestParseFileList(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  int
	}{
		{"plain array", `["a.go", "b.go"]`, 2},
		{"with code block", "```json\n[\"a.go\"]\n```", 1},
		{"embedded in text", "Here are the files: [\"x.go\", \"y.go\"] that matter", 2},
		{"invalid", "no json here", 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			files, err := parseFileList(tt.input)
			if tt.want == 0 {
				if err == nil {
					t.Error("expected error for invalid input")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(files) != tt.want {
				t.Errorf("expected %d files, got %d: %v", tt.want, len(files), files)
			}
		})
	}
}
