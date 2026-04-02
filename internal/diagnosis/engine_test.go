package diagnosis

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"slack-issue-bot/internal/llm"
)

type mockLLM struct {
	resp llm.DiagnoseResponse
	err  error
	got  llm.DiagnoseRequest
}

func (m *mockLLM) Name() string { return "mock" }
func (m *mockLLM) Diagnose(ctx context.Context, req llm.DiagnoseRequest) (llm.DiagnoseResponse, error) {
	m.got = req
	return m.resp, m.err
}

func TestEngine_Diagnose(t *testing.T) {
	repoDir := t.TempDir()
	os.MkdirAll(filepath.Join(repoDir, "src"), 0755)
	os.WriteFile(filepath.Join(repoDir, "src", "login.go"), []byte("package auth\n\nfunc Login() {}"), 0644)

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
		Keywords: []string{"login"},
	})
	if err != nil {
		t.Fatalf("Diagnose failed: %v", err)
	}
	if resp.Summary != "login function missing validation" {
		t.Errorf("unexpected summary: %s", resp.Summary)
	}
	if len(mock.got.RepoFiles) == 0 {
		t.Error("expected repo files to be passed to LLM")
	}
}
