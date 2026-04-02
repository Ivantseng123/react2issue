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

// Diagnose runs an agent-style loop to explore the repo and produce a diagnosis.
//
// Flow:
//  1. Try keyword grep (free, no LLM call)
//  2. If grep misses → ask LLM to suggest search terms → grep those
//  3. If still no files → ask LLM to pick from repo file tree
//  4. Read selected files → ask LLM for final diagnosis
func (e *Engine) Diagnose(ctx context.Context, input DiagnoseInput) (llm.DiagnoseResponse, error) {
	// --- Step 1: keyword grep (free) ---
	files, _ := e.grepFiles(input.RepoPath, input.Keywords)
	if len(files) > 0 {
		slog.Info("agent: keyword grep hit", "files", len(files))
		return e.diagnoseWithFiles(ctx, input, files)
	}

	// --- Step 2: ask LLM for search terms → grep those ---
	slog.Info("agent: keyword grep miss, asking LLM for search terms")
	searchTerms, err := e.llmSuggestSearchTerms(ctx, input)
	if err != nil {
		slog.Warn("agent: LLM search term suggestion failed", "error", err)
	} else if len(searchTerms) > 0 {
		slog.Info("agent: LLM suggested search terms", "terms", searchTerms)
		files, _ = e.grepFiles(input.RepoPath, searchTerms)
		if len(files) > 0 {
			slog.Info("agent: LLM-suggested grep hit", "files", len(files))
			return e.diagnoseWithFiles(ctx, input, files)
		}
	}

	// --- Step 3: no grep hits at all → LLM picks from file tree ---
	slog.Info("agent: grep still empty, asking LLM to pick from file tree")
	tree := e.repoTree(input.RepoPath)
	picked, err := e.llmPickFiles(ctx, input, tree)
	if err != nil {
		slog.Warn("agent: LLM file picker failed", "error", err)
		// Last resort: diagnose with tree as context
		return e.diagnoseWithTree(ctx, input, tree)
	}

	slog.Info("agent: LLM picked files", "files", picked)
	return e.diagnoseWithFiles(ctx, input, picked)
}

// --- Agent turns ---

// llmSuggestSearchTerms asks the LLM to translate/extract code-searchable keywords from the message.
func (e *Engine) llmSuggestSearchTerms(ctx context.Context, input DiagnoseInput) ([]string, error) {
	prompt := fmt.Sprintf(`A user reported the following in a Slack channel:

"%s"

This is a %s report for a software codebase. The message may be in a non-English language.
Your job: suggest 5-10 English keywords or code identifiers that are likely to appear in the source code related to this report.

Think about: class names, function names, variable names, file names, module names, database table names, API endpoints, etc.

Return ONLY a JSON array of strings. Example: ["reinsurance", "cession", "PolicyResult", "calculatePremium"]`, input.Message, input.Type)

	req := llm.DiagnoseRequest{
		Type:    input.Type,
		Message: prompt,
		Prompt:  input.Prompt,
	}

	resp, err := e.llmProvider.Diagnose(ctx, req)
	if err != nil {
		return nil, err
	}

	return parseStringArray(resp.Summary)
}

// llmPickFiles asks the LLM to select relevant files from the repo tree.
func (e *Engine) llmPickFiles(ctx context.Context, input DiagnoseInput, tree string) ([]string, error) {
	prompt := fmt.Sprintf(`A user reported the following:

"%s"

Below is the file listing of the repository. Select the files most likely related to this %s report.

Return ONLY a JSON array of file paths. Pick at most %d files. Focus on source code files.

Repository files:
%s`, input.Message, input.Type, e.maxFiles, tree)

	req := llm.DiagnoseRequest{
		Type:    input.Type,
		Message: prompt,
		Prompt:  input.Prompt,
	}

	resp, err := e.llmProvider.Diagnose(ctx, req)
	if err != nil {
		return nil, err
	}

	return parseStringArray(resp.Summary)
}

// diagnoseWithFiles reads the given files and sends them to the LLM for final diagnosis.
func (e *Engine) diagnoseWithFiles(ctx context.Context, input DiagnoseInput, files []string) (llm.DiagnoseResponse, error) {
	repoFiles := e.readFiles(input.RepoPath, files)
	req := llm.DiagnoseRequest{
		Type:      input.Type,
		Message:   input.Message,
		RepoFiles: repoFiles,
		Prompt:    input.Prompt,
	}
	resp, err := e.llmProvider.Diagnose(ctx, req)
	if err != nil {
		return llm.DiagnoseResponse{}, fmt.Errorf("diagnosis: %w", err)
	}
	return resp, nil
}

// diagnoseWithTree sends the file tree (not contents) as last-resort context.
func (e *Engine) diagnoseWithTree(ctx context.Context, input DiagnoseInput, tree string) (llm.DiagnoseResponse, error) {
	repoFiles := []llm.File{{
		Path:    "REPO_FILE_LIST.txt",
		Content: "This is the repository file listing (not file contents). Use file names and paths to infer which files are related.\n\n" + tree,
	}}
	req := llm.DiagnoseRequest{
		Type:      input.Type,
		Message:   input.Message,
		RepoFiles: repoFiles,
		Prompt:    input.Prompt,
	}
	resp, err := e.llmProvider.Diagnose(ctx, req)
	if err != nil {
		return llm.DiagnoseResponse{}, fmt.Errorf("diagnosis with tree: %w", err)
	}
	return resp, nil
}

// --- Tool implementations (executed by the engine, not the LLM) ---

// grepFiles searches the repo for files matching any of the given terms.
func (e *Engine) grepFiles(repoPath string, terms []string) ([]string, error) {
	seen := make(map[string]int)
	for _, term := range terms {
		// Case-insensitive grep
		cmd := exec.Command("git", "-C", repoPath, "grep", "-rli", "--no-color", term)
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

// readFiles reads file contents from disk.
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

// FindFiles returns relevant file references without calling the LLM.
// Used in lite mode to produce a handoff spec.
func (e *Engine) FindFiles(input DiagnoseInput) []llm.FileRef {
	files, _ := e.grepFiles(input.RepoPath, input.Keywords)
	var refs []llm.FileRef
	for _, f := range files {
		refs = append(refs, llm.FileRef{Path: f, Description: "matched keywords from Slack message"})
	}
	return refs
}

// --- Helpers ---

// parseStringArray extracts a JSON string array from LLM text output.
func parseStringArray(text string) ([]string, error) {
	text = strings.TrimSpace(text)
	var arr []string

	// Direct parse
	if err := json.Unmarshal([]byte(text), &arr); err == nil && len(arr) > 0 {
		return arr, nil
	}

	// Extract from code block
	if idx := strings.Index(text, "```"); idx != -1 {
		start := idx + 3
		if nl := strings.Index(text[start:], "\n"); nl != -1 {
			start += nl + 1
		}
		if end := strings.Index(text[start:], "```"); end != -1 {
			if err := json.Unmarshal([]byte(strings.TrimSpace(text[start:start+end])), &arr); err == nil && len(arr) > 0 {
				return arr, nil
			}
		}
	}

	// Find array anywhere
	if idx := strings.Index(text, "["); idx != -1 {
		if end := strings.LastIndex(text, "]"); end > idx {
			if err := json.Unmarshal([]byte(text[idx:end+1]), &arr); err == nil && len(arr) > 0 {
				return arr, nil
			}
		}
	}

	return nil, fmt.Errorf("could not parse string array from: %s", truncate(text, 100))
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
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
