package diagnosis

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"slack-issue-bot/internal/llm"
)

type DiagnoseInput struct {
	Type     string
	Message  string
	RepoPath string
	Keywords []string
}

type Engine struct {
	llmProvider llm.Provider
	maxFiles    int
}

func NewEngine(provider llm.Provider, maxFiles int) *Engine {
	if maxFiles <= 0 {
		maxFiles = 10
	}
	return &Engine{
		llmProvider: provider,
		maxFiles:    maxFiles,
	}
}

func (e *Engine) Diagnose(ctx context.Context, input DiagnoseInput) (llm.DiagnoseResponse, error) {
	files, err := e.findRelevantFiles(input.RepoPath, input.Keywords)
	if err != nil {
		slog.Warn("failed to grep repo for relevant files", "error", err)
	}

	var repoFiles []llm.File
	for _, f := range files {
		fullPath := filepath.Join(input.RepoPath, f)
		content, err := os.ReadFile(fullPath)
		if err != nil {
			continue
		}
		lines := strings.Split(string(content), "\n")
		if len(lines) > 200 {
			lines = lines[:200]
		}
		repoFiles = append(repoFiles, llm.File{
			Path:    f,
			Content: strings.Join(lines, "\n"),
		})
	}

	req := llm.DiagnoseRequest{
		Type:      input.Type,
		Message:   input.Message,
		RepoFiles: repoFiles,
	}

	resp, err := e.llmProvider.Diagnose(ctx, req)
	if err != nil {
		return llm.DiagnoseResponse{}, fmt.Errorf("llm diagnose: %w", err)
	}
	return resp, nil
}

func (e *Engine) findRelevantFiles(repoPath string, keywords []string) ([]string, error) {
	seen := make(map[string]int)
	for _, kw := range keywords {
		cmd := exec.Command("git", "-C", repoPath, "grep", "-rl", "--no-color", kw)
		out, err := cmd.Output()
		if err != nil {
			continue
		}
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if line != "" && !shouldSkipFile(line) {
				seen[line]++
			}
		}
	}

	// Fallback: if git grep found nothing (e.g. not a git repo), walk the filesystem.
	if len(seen) == 0 {
		_ = filepath.Walk(repoPath, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil
			}
			rel, err := filepath.Rel(repoPath, path)
			if err != nil || shouldSkipFile(rel) {
				return nil
			}
			content, err := os.ReadFile(path)
			if err != nil {
				return nil
			}
			lower := strings.ToLower(string(content))
			for _, kw := range keywords {
				if strings.Contains(lower, strings.ToLower(kw)) {
					seen[rel]++
				}
			}
			return nil
		})
	}

	type scored struct {
		path  string
		score int
	}
	var files []scored
	for p, s := range seen {
		files = append(files, scored{p, s})
	}
	for i := range files {
		for j := i + 1; j < len(files); j++ {
			if files[j].score > files[i].score {
				files[i], files[j] = files[j], files[i]
			}
		}
	}

	var result []string
	for i, f := range files {
		if i >= e.maxFiles {
			break
		}
		result = append(result, f.path)
	}
	return result, nil
}

func shouldSkipFile(path string) bool {
	skip := []string{".min.js", ".min.css", "vendor/", "node_modules/", ".lock", "go.sum", "package-lock"}
	for _, s := range skip {
		if strings.Contains(path, s) {
			return true
		}
	}
	return false
}
