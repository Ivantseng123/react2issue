package diagnosis

import (
	"context"
	"encoding/json"
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
	Prompt   llm.PromptOptions
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
	// Step 1: try keyword grep first
	files, _ := e.findRelevantFiles(input.RepoPath, input.Keywords)

	// Step 2: if grep found nothing, use two-pass LLM approach
	if len(files) == 0 {
		slog.Info("no keyword matches, using two-pass LLM file selection")
		tree := e.repoTree(input.RepoPath)

		picked, err := e.llmPickFiles(ctx, input, tree)
		if err != nil {
			slog.Warn("LLM file picker failed", "error", err)
		} else {
			files = picked
			slog.Info("LLM picked files", "count", len(files), "files", files)
		}
	}

	// Step 3: read selected files
	repoFiles := e.readFiles(input.RepoPath, files)

	// Step 4: final diagnosis with file contents
	req := llm.DiagnoseRequest{
		Type:      input.Type,
		Message:   input.Message,
		RepoFiles: repoFiles,
		Prompt:    input.Prompt,
	}

	resp, err := e.llmProvider.Diagnose(ctx, req)
	if err != nil {
		return llm.DiagnoseResponse{}, fmt.Errorf("llm diagnose: %w", err)
	}
	return resp, nil
}

// llmPickFiles is Pass 1: give the LLM the file tree, ask it to pick relevant files.
func (e *Engine) llmPickFiles(ctx context.Context, input DiagnoseInput, tree string) ([]string, error) {
	prompt := fmt.Sprintf(`You are a senior software engineer. A user reported the following:

"%s"

Below is the file listing of the repository. Select the files most likely related to this %s report.

Return ONLY a JSON array of file paths, nothing else. Example: ["src/auth/login.go", "src/models/user.go"]
Pick at most %d files. Focus on source code files (not configs, tests, or docs).

Repository files:
%s`, input.Message, input.Type, e.maxFiles, tree)

	req := llm.DiagnoseRequest{
		Type:    input.Type,
		Message: prompt,
		Prompt:  input.Prompt,
	}

	resp, err := e.llmProvider.Diagnose(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("file picker call: %w", err)
	}

	// The response might be in the Summary (raw text fallback) or structured.
	// Try to parse a JSON array from whatever we got back.
	text := resp.Summary
	return parseFileList(text)
}

// parseFileList extracts a JSON string array from LLM output.
func parseFileList(text string) ([]string, error) {
	// Try direct JSON array parse
	var files []string
	if err := json.Unmarshal([]byte(strings.TrimSpace(text)), &files); err == nil && len(files) > 0 {
		return files, nil
	}

	// Try extracting from markdown code block
	if idx := strings.Index(text, "```"); idx != -1 {
		start := idx + 3
		// skip optional language tag (e.g. ```json)
		if nl := strings.Index(text[start:], "\n"); nl != -1 {
			start += nl + 1
		}
		if end := strings.Index(text[start:], "```"); end != -1 {
			if err := json.Unmarshal([]byte(strings.TrimSpace(text[start:start+end])), &files); err == nil && len(files) > 0 {
				return files, nil
			}
		}
	}

	// Try finding array anywhere in text
	if idx := strings.Index(text, "["); idx != -1 {
		if end := strings.LastIndex(text, "]"); end > idx {
			if err := json.Unmarshal([]byte(text[idx:end+1]), &files); err == nil && len(files) > 0 {
				return files, nil
			}
		}
	}

	return nil, fmt.Errorf("could not parse file list from LLM response")
}

// readFiles reads file contents from disk given relative paths.
func (e *Engine) readFiles(repoPath string, files []string) []llm.File {
	var result []llm.File
	for _, f := range files {
		fullPath := filepath.Join(repoPath, f)
		content, err := os.ReadFile(fullPath)
		if err != nil {
			slog.Debug("skipping unreadable file", "path", f, "error", err)
			continue
		}
		lines := strings.Split(string(content), "\n")
		if len(lines) > 200 {
			lines = lines[:200]
		}
		result = append(result, llm.File{
			Path:    f,
			Content: strings.Join(lines, "\n"),
		})
	}
	return result
}

// FindFiles returns relevant file references without calling the LLM.
// Used in lite mode to produce a handoff spec.
func (e *Engine) FindFiles(input DiagnoseInput) []llm.FileRef {
	files, _ := e.findRelevantFiles(input.RepoPath, input.Keywords)
	var refs []llm.FileRef
	for _, f := range files {
		refs = append(refs, llm.FileRef{Path: f, Description: "matched keywords from Slack message"})
	}
	return refs
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

// repoTree generates a file listing via `git ls-files`.
func (e *Engine) repoTree(repoPath string) string {
	cmd := exec.Command("git", "-C", repoPath, "ls-files")
	out, err := cmd.Output()
	if err == nil && len(out) > 0 {
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		if len(lines) > 500 {
			lines = append(lines[:500], fmt.Sprintf("... and %d more files", len(lines)-500))
		}
		return strings.Join(lines, "\n")
	}

	// Fallback: walk filesystem
	var lines []string
	filepath.Walk(repoPath, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(repoPath, path)
		if shouldSkipFile(rel) {
			return nil
		}
		lines = append(lines, rel)
		if len(lines) >= 500 {
			return filepath.SkipAll
		}
		return nil
	})
	return strings.Join(lines, "\n")
}

func shouldSkipFile(path string) bool {
	skip := []string{".min.js", ".min.css", "vendor/", "node_modules/", ".lock", "go.sum", "package-lock", ".class", ".jar", "target/", "build/", ".git/"}
	for _, s := range skip {
		if strings.Contains(path, s) {
			return true
		}
	}
	return false
}
