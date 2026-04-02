package diagnosis

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"slack-issue-bot/internal/llm"
)

// Tool is the interface that each diagnosis tool implements.
type Tool interface {
	Name() string
	Description() string
	Schema() llm.ToolDef
	Execute(repoPath string, args json.RawMessage) (string, error)
}

// AllTools returns all available diagnosis tools.
func AllTools() []Tool {
	return []Tool{
		&GrepTool{},
		&ReadFileTool{},
		&ListFilesTool{},
		&ReadContextTool{},
		&SearchCodeTool{},
		&GitLogTool{},
	}
}

// ToolDefs extracts ToolDef schemas from a slice of tools.
func ToolDefs(tools []Tool) []llm.ToolDef {
	defs := make([]llm.ToolDef, len(tools))
	for i, t := range tools {
		defs[i] = t.Schema()
	}
	return defs
}

// ToolMap builds a name->Tool lookup map.
func ToolMap(tools []Tool) map[string]Tool {
	m := make(map[string]Tool, len(tools))
	for _, t := range tools {
		m[t.Name()] = t
	}
	return m
}

// shouldSkipToolFile returns true for files that should be excluded from results.
func shouldSkipToolFile(path string) bool {
	skip := []string{".min.js", "vendor/", "node_modules/", ".lock", "go.sum", "target/", "build/", ".git/"}
	for _, s := range skip {
		if strings.Contains(path, s) {
			return true
		}
	}
	return false
}

// ---------- GrepTool ----------

// GrepTool searches for files matching a pattern using git grep.
type GrepTool struct{}

func (g *GrepTool) Name() string        { return "grep" }
func (g *GrepTool) Description() string  { return "Search for files containing a pattern. Returns a scored list of matching file paths." }

func (g *GrepTool) Schema() llm.ToolDef {
	return llm.ToolDef{
		Name:        g.Name(),
		Description: g.Description(),
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pattern": map[string]any{
					"type":        "string",
					"description": "Search pattern (case-insensitive). Max 200 characters.",
				},
				"max_results": map[string]any{
					"type":        "integer",
					"description": "Maximum number of files to return (default 10).",
				},
			},
			"required": []string{"pattern"},
		},
	}
}

type grepArgs struct {
	Pattern    string `json:"pattern"`
	MaxResults int    `json:"max_results"`
}

func (g *GrepTool) Execute(repoPath string, raw json.RawMessage) (string, error) {
	var args grepArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	if args.Pattern == "" {
		return "Error: pattern is required", nil
	}
	if len(args.Pattern) > 200 {
		args.Pattern = args.Pattern[:200]
	}
	if args.MaxResults <= 0 {
		args.MaxResults = 10
	}

	cmd := exec.Command("git", "-C", repoPath, "grep", "-rli", "--no-color", args.Pattern)
	out, err := cmd.Output()
	if err != nil {
		// git grep returns exit 1 when no match — not an error for us.
		return "No matches found.", nil
	}

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	// Score by occurrence and filter skippable files.
	seen := make(map[string]int)
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" && !shouldSkipToolFile(line) {
			seen[line]++
		}
	}

	type scored struct {
		path  string
		score int
	}
	var results []scored
	for p, s := range seen {
		results = append(results, scored{p, s})
	}
	// Sort by score descending, then path for stability.
	for i := range results {
		for j := i + 1; j < len(results); j++ {
			if results[j].score > results[i].score ||
				(results[j].score == results[i].score && results[j].path < results[i].path) {
				results[i], results[j] = results[j], results[i]
			}
		}
	}

	if len(results) == 0 {
		return "No matches found.", nil
	}

	var sb strings.Builder
	for i, r := range results {
		if i >= args.MaxResults {
			break
		}
		sb.WriteString(r.path)
		sb.WriteString("\n")
	}
	return strings.TrimSpace(sb.String()), nil
}

// ---------- ReadFileTool ----------

// ReadFileTool reads file contents with line numbers.
type ReadFileTool struct{}

func (r *ReadFileTool) Name() string        { return "read_file" }
func (r *ReadFileTool) Description() string  { return "Read file contents with line numbers. Returns up to max_lines lines." }

func (r *ReadFileTool) Schema() llm.ToolDef {
	return llm.ToolDef{
		Name:        r.Name(),
		Description: r.Description(),
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Relative file path within the repository.",
				},
				"max_lines": map[string]any{
					"type":        "integer",
					"description": "Maximum number of lines to return (default 200).",
				},
			},
			"required": []string{"path"},
		},
	}
}

type readFileArgs struct {
	Path     string `json:"path"`
	MaxLines int    `json:"max_lines"`
}

func (r *ReadFileTool) Execute(repoPath string, raw json.RawMessage) (string, error) {
	var args readFileArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	if args.Path == "" {
		return "Error: path is required", nil
	}
	if args.MaxLines <= 0 {
		args.MaxLines = 200
	}

	fullPath := filepath.Join(repoPath, args.Path)
	content, err := os.ReadFile(fullPath)
	if err != nil {
		return "Error: file not found", nil
	}

	lines := strings.Split(string(content), "\n")
	if len(lines) > args.MaxLines {
		lines = lines[:args.MaxLines]
	}

	var sb strings.Builder
	for i, line := range lines {
		sb.WriteString(fmt.Sprintf("%4d | %s\n", i+1, line))
	}
	return strings.TrimRight(sb.String(), "\n"), nil
}

// ---------- ListFilesTool ----------

// ListFilesTool lists files in the repository, optionally filtered by pattern.
type ListFilesTool struct{}

func (l *ListFilesTool) Name() string        { return "list_files" }
func (l *ListFilesTool) Description() string  { return "List files in the repository. Optionally filter by glob pattern." }

func (l *ListFilesTool) Schema() llm.ToolDef {
	return llm.ToolDef{
		Name:        l.Name(),
		Description: l.Description(),
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pattern": map[string]any{
					"type":        "string",
					"description": "Optional glob pattern to filter files (e.g. '*.go', 'src/**').",
				},
			},
		},
	}
}

type listFilesArgs struct {
	Pattern string `json:"pattern"`
}

func (l *ListFilesTool) Execute(repoPath string, raw json.RawMessage) (string, error) {
	var args listFilesArgs
	if raw != nil && string(raw) != "null" && string(raw) != "{}" {
		if err := json.Unmarshal(raw, &args); err != nil {
			return "", fmt.Errorf("invalid args: %w", err)
		}
	}

	var cmdArgs []string
	cmdArgs = append(cmdArgs, "-C", repoPath, "ls-files")
	if args.Pattern != "" {
		cmdArgs = append(cmdArgs, args.Pattern)
	}

	cmd := exec.Command("git", cmdArgs...)
	out, err := cmd.Output()
	if err == nil && len(out) > 0 {
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		if len(lines) > 500 {
			lines = append(lines[:500], fmt.Sprintf("... and %d more files", len(lines)-500))
		}
		return strings.Join(lines, "\n"), nil
	}

	// Fallback to filepath.Walk.
	var lines []string
	filepath.Walk(repoPath, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil || info.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(repoPath, path)
		if shouldSkipToolFile(rel) {
			return nil
		}
		lines = append(lines, rel)
		if len(lines) >= 500 {
			return filepath.SkipAll
		}
		return nil
	})

	if len(lines) == 0 {
		return "No files found.", nil
	}
	return strings.Join(lines, "\n"), nil
}

// ---------- ReadContextTool ----------

// ReadContextTool reads repository context documents (README, CLAUDE.md, etc.).
type ReadContextTool struct{}

func (rc *ReadContextTool) Name() string        { return "read_context" }
func (rc *ReadContextTool) Description() string  { return "Read repository context documents (README.md, CLAUDE.md, agent.md, AGENTS.md). No input arguments needed." }

func (rc *ReadContextTool) Schema() llm.ToolDef {
	return llm.ToolDef{
		Name:        rc.Name(),
		Description: rc.Description(),
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
	}
}

func (rc *ReadContextTool) Execute(repoPath string, _ json.RawMessage) (string, error) {
	candidates := []string{"README.md", "readme.md", "CLAUDE.md", "agent.md", "AGENTS.md"}
	var sb strings.Builder
	found := false

	for _, name := range candidates {
		fullPath := filepath.Join(repoPath, name)
		content, err := os.ReadFile(fullPath)
		if err != nil {
			continue
		}
		found = true
		lines := strings.Split(string(content), "\n")
		if len(lines) > 100 {
			lines = lines[:100]
		}
		sb.WriteString(fmt.Sprintf("=== %s ===\n", name))
		sb.WriteString(strings.Join(lines, "\n"))
		sb.WriteString("\n\n")
	}

	if !found {
		return "No context documents found.", nil
	}
	return strings.TrimSpace(sb.String()), nil
}

// ---------- SearchCodeTool ----------

// SearchCodeTool searches for a regex pattern with context lines.
type SearchCodeTool struct{}

func (s *SearchCodeTool) Name() string        { return "search_code" }
func (s *SearchCodeTool) Description() string  { return "Search for a regex pattern in code with surrounding context lines." }

func (s *SearchCodeTool) Schema() llm.ToolDef {
	return llm.ToolDef{
		Name:        s.Name(),
		Description: s.Description(),
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pattern": map[string]any{
					"type":        "string",
					"description": "Extended regex pattern to search for. Max 200 characters.",
				},
				"context_lines": map[string]any{
					"type":        "integer",
					"description": "Number of context lines around each match (default 2).",
				},
			},
			"required": []string{"pattern"},
		},
	}
}

type searchCodeArgs struct {
	Pattern      string `json:"pattern"`
	ContextLines int    `json:"context_lines"`
}

func (s *SearchCodeTool) Execute(repoPath string, raw json.RawMessage) (string, error) {
	var args searchCodeArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	if args.Pattern == "" {
		return "Error: pattern is required", nil
	}
	if len(args.Pattern) > 200 {
		args.Pattern = args.Pattern[:200]
	}
	if args.ContextLines <= 0 {
		args.ContextLines = 2
	}

	contextFlag := fmt.Sprintf("-C%d", args.ContextLines)
	cmd := exec.Command("git", "-C", repoPath, "grep", "-n", "-E", contextFlag, args.Pattern)
	out, err := cmd.Output()
	if err != nil {
		return "No matches found.", nil
	}

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) > 200 {
		lines = append(lines[:200], fmt.Sprintf("... output truncated (%d lines total)", len(lines)))
	}
	return strings.Join(lines, "\n"), nil
}

// ---------- GitLogTool ----------

// GitLogTool shows recent git log entries.
type GitLogTool struct{}

func (gl *GitLogTool) Name() string        { return "git_log" }
func (gl *GitLogTool) Description() string  { return "Show recent git commit log (one-line format). Optionally filter by file path." }

func (gl *GitLogTool) Schema() llm.ToolDef {
	return llm.ToolDef{
		Name:        gl.Name(),
		Description: gl.Description(),
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"count": map[string]any{
					"type":        "integer",
					"description": "Number of log entries to show (default 20).",
				},
				"path": map[string]any{
					"type":        "string",
					"description": "Optional file path to filter commits.",
				},
			},
		},
	}
}

type gitLogArgs struct {
	Count int    `json:"count"`
	Path  string `json:"path"`
}

func (gl *GitLogTool) Execute(repoPath string, raw json.RawMessage) (string, error) {
	var args gitLogArgs
	if raw != nil && string(raw) != "null" && string(raw) != "{}" {
		if err := json.Unmarshal(raw, &args); err != nil {
			return "", fmt.Errorf("invalid args: %w", err)
		}
	}
	if args.Count <= 0 {
		args.Count = 20
	}

	cmdArgs := []string{"-C", repoPath, "log", "--oneline", fmt.Sprintf("-n%d", args.Count)}
	if args.Path != "" {
		cmdArgs = append(cmdArgs, "--", args.Path)
	}

	cmd := exec.Command("git", cmdArgs...)
	out, err := cmd.Output()
	if err != nil {
		return "No git log available.", nil
	}

	result := strings.TrimSpace(string(out))
	if result == "" {
		return "No commits found.", nil
	}
	return result, nil
}
