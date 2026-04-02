package diagnosis

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

// ---------- GrepTool ----------

func TestGrepTool_BasicMatch(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}

	repo := initGitRepo(t, map[string]string{
		"src/auth/login.go":  "package auth\n\nfunc Login() { fmt.Println(\"hello\") }",
		"src/auth/logout.go": "package auth\n\nfunc Logout() {}",
		"src/models/user.go": "package models\n\ntype User struct{ Name string }",
	})

	tool := &GrepTool{}
	args, _ := json.Marshal(grepArgs{Pattern: "Login"})

	result, err := tool.Execute(repo, args)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if !strings.Contains(result, "login.go") {
		t.Errorf("expected login.go in results, got: %s", result)
	}
	// logout.go should NOT match "Login" (case-insensitive grep will match "Logout" only if pattern is "Login")
	// Actually git grep -i would match. The tool uses -li (case-insensitive), so this is about grep behavior.
}

func TestGrepTool_NoMatch(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}

	repo := initGitRepo(t, map[string]string{
		"src/main.go": "package main\n\nfunc main() {}",
	})

	tool := &GrepTool{}
	args, _ := json.Marshal(grepArgs{Pattern: "nonexistent_xyz_123"})

	result, err := tool.Execute(repo, args)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if result != "No matches found." {
		t.Errorf("expected 'No matches found.', got: %s", result)
	}
}

func TestGrepTool_SkipsVendor(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}

	repo := initGitRepo(t, map[string]string{
		"src/main.go":           "package main\n\nfunc DoStuff() {}",
		"vendor/lib/helper.go":  "package lib\n\nfunc DoStuff() {}",
		"node_modules/x/lib.js": "function DoStuff() {}",
	})

	tool := &GrepTool{}
	args, _ := json.Marshal(grepArgs{Pattern: "DoStuff"})

	result, err := tool.Execute(repo, args)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if strings.Contains(result, "vendor/") {
		t.Errorf("vendor/ files should be skipped, got: %s", result)
	}
	if strings.Contains(result, "node_modules/") {
		t.Errorf("node_modules/ files should be skipped, got: %s", result)
	}
	if !strings.Contains(result, "src/main.go") {
		t.Errorf("expected src/main.go in results, got: %s", result)
	}
}

func TestGrepTool_MaxResults(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}

	files := map[string]string{}
	for i := 0; i < 5; i++ {
		files[strings.Replace("src/file_N.go", "N", string(rune('a'+i)), 1)] = "package src\n\nfunc Handler() {}"
	}
	repo := initGitRepo(t, files)

	tool := &GrepTool{}
	args, _ := json.Marshal(grepArgs{Pattern: "Handler", MaxResults: 2})

	result, err := tool.Execute(repo, args)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(result), "\n")
	if len(lines) > 2 {
		t.Errorf("expected at most 2 results, got %d: %s", len(lines), result)
	}
}

// ---------- ReadFileTool ----------

func TestReadFileTool_Success(t *testing.T) {
	repo := initGitRepo(t, map[string]string{
		"src/main.go": "package main\n\nimport \"fmt\"\n\nfunc main() {\n\tfmt.Println(\"hello\")\n}",
	})

	tool := &ReadFileTool{}
	args, _ := json.Marshal(readFileArgs{Path: "src/main.go"})

	result, err := tool.Execute(repo, args)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if !strings.Contains(result, "1 | package main") {
		t.Errorf("expected line-numbered output, got: %s", result)
	}
	if !strings.Contains(result, "fmt.Println") {
		t.Errorf("expected file content, got: %s", result)
	}
}

func TestReadFileTool_NotFound(t *testing.T) {
	repo := initGitRepo(t, map[string]string{
		"src/main.go": "package main",
	})

	tool := &ReadFileTool{}
	args, _ := json.Marshal(readFileArgs{Path: "nonexistent.go"})

	result, err := tool.Execute(repo, args)
	if err != nil {
		t.Fatalf("Execute should not return Go error: %v", err)
	}
	if result != "Error: file not found" {
		t.Errorf("expected 'Error: file not found', got: %s", result)
	}
}

func TestReadFileTool_MaxLines(t *testing.T) {
	var sb strings.Builder
	for i := 0; i < 50; i++ {
		sb.WriteString("// line\n")
	}
	repo := initGitRepo(t, map[string]string{
		"big.go": sb.String(),
	})

	tool := &ReadFileTool{}
	args, _ := json.Marshal(readFileArgs{Path: "big.go", MaxLines: 5})

	result, err := tool.Execute(repo, args)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	lines := strings.Split(strings.TrimRight(result, "\n"), "\n")
	if len(lines) != 5 {
		t.Errorf("expected 5 lines, got %d", len(lines))
	}
}

// ---------- ListFilesTool ----------

func TestListFilesTool_All(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}

	repo := initGitRepo(t, map[string]string{
		"src/main.go":  "package main",
		"src/util.go":  "package main",
		"docs/help.md": "# Help",
	})

	tool := &ListFilesTool{}
	result, err := tool.Execute(repo, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if !strings.Contains(result, "src/main.go") {
		t.Errorf("expected src/main.go in listing, got: %s", result)
	}
	if !strings.Contains(result, "docs/help.md") {
		t.Errorf("expected docs/help.md in listing, got: %s", result)
	}
}

func TestListFilesTool_Pattern(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}

	repo := initGitRepo(t, map[string]string{
		"src/main.go":  "package main",
		"src/util.go":  "package main",
		"docs/help.md": "# Help",
	})

	tool := &ListFilesTool{}
	args, _ := json.Marshal(listFilesArgs{Pattern: "*.go"})
	result, err := tool.Execute(repo, args)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if !strings.Contains(result, ".go") {
		t.Errorf("expected .go files, got: %s", result)
	}
	if strings.Contains(result, "help.md") {
		t.Errorf("should not include .md files with *.go pattern, got: %s", result)
	}
}

// ---------- ReadContextTool ----------

func TestReadContextTool_Found(t *testing.T) {
	repo := initGitRepo(t, map[string]string{
		"README.md":  "# My Project\n\nThis is a test project.",
		"CLAUDE.md":  "# Claude Config\n\nSome instructions.",
		"src/main.go": "package main",
	})

	tool := &ReadContextTool{}
	result, err := tool.Execute(repo, nil)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if !strings.Contains(result, "=== README.md ===") {
		t.Errorf("expected README.md section, got: %s", result)
	}
	if !strings.Contains(result, "=== CLAUDE.md ===") {
		t.Errorf("expected CLAUDE.md section, got: %s", result)
	}
	if !strings.Contains(result, "My Project") {
		t.Errorf("expected README content, got: %s", result)
	}
}

func TestReadContextTool_NoneFound(t *testing.T) {
	repo := initGitRepo(t, map[string]string{
		"src/main.go": "package main",
	})

	tool := &ReadContextTool{}
	result, err := tool.Execute(repo, nil)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if result != "No context documents found." {
		t.Errorf("expected 'No context documents found.', got: %s", result)
	}
}

// ---------- SearchCodeTool ----------

func TestSearchCodeTool_BasicMatch(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}

	repo := initGitRepo(t, map[string]string{
		"src/handler.go": "package src\n\nfunc HandleRequest() {\n\t// process request\n\treturn nil\n}",
	})

	tool := &SearchCodeTool{}
	args, _ := json.Marshal(searchCodeArgs{Pattern: "HandleRequest"})

	result, err := tool.Execute(repo, args)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if !strings.Contains(result, "HandleRequest") {
		t.Errorf("expected HandleRequest in output, got: %s", result)
	}
	// Should include line numbers from -n flag.
	if !strings.Contains(result, ":") {
		t.Errorf("expected colon-separated line numbers, got: %s", result)
	}
}

func TestSearchCodeTool_NoMatch(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}

	repo := initGitRepo(t, map[string]string{
		"src/main.go": "package main\n\nfunc main() {}",
	})

	tool := &SearchCodeTool{}
	args, _ := json.Marshal(searchCodeArgs{Pattern: "ZZZ_no_match_999"})

	result, err := tool.Execute(repo, args)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if result != "No matches found." {
		t.Errorf("expected 'No matches found.', got: %s", result)
	}
}

// ---------- GitLogTool ----------

func TestGitLogTool_Default(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}

	repo := initGitRepo(t, map[string]string{
		"src/main.go": "package main",
	})

	tool := &GitLogTool{}
	result, err := tool.Execute(repo, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if !strings.Contains(result, "init") {
		t.Errorf("expected 'init' commit message, got: %s", result)
	}
}

func TestGitLogTool_WithPath(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}

	repo := initGitRepo(t, map[string]string{
		"src/a.go": "package src",
		"src/b.go": "package src",
	})

	tool := &GitLogTool{}
	args, _ := json.Marshal(gitLogArgs{Path: "src/a.go", Count: 5})

	result, err := tool.Execute(repo, args)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if result == "" || result == "No git log available." {
		t.Errorf("expected log output, got: %s", result)
	}
}

func TestGitLogTool_Count(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}

	repo := initGitRepo(t, map[string]string{
		"file.txt": "v1",
	})

	// Add a second commit.
	runGit(t, repo, "commit", "--allow-empty", "-m", "second commit")
	runGit(t, repo, "commit", "--allow-empty", "-m", "third commit")

	tool := &GitLogTool{}
	args, _ := json.Marshal(gitLogArgs{Count: 2})

	result, err := tool.Execute(repo, args)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(result), "\n")
	if len(lines) != 2 {
		t.Errorf("expected 2 log entries, got %d: %s", len(lines), result)
	}
}

// ---------- Helper function tests ----------

func TestAllTools(t *testing.T) {
	tools := AllTools()
	if len(tools) != 6 {
		t.Errorf("expected 6 tools, got %d", len(tools))
	}

	names := make(map[string]bool)
	for _, tool := range tools {
		names[tool.Name()] = true
	}

	expected := []string{"grep", "read_file", "list_files", "read_context", "search_code", "git_log"}
	for _, name := range expected {
		if !names[name] {
			t.Errorf("missing tool: %s", name)
		}
	}
}

func TestToolDefs(t *testing.T) {
	tools := AllTools()
	defs := ToolDefs(tools)
	if len(defs) != len(tools) {
		t.Errorf("expected %d defs, got %d", len(tools), len(defs))
	}
	for i, def := range defs {
		if def.Name != tools[i].Name() {
			t.Errorf("def[%d].Name = %q, want %q", i, def.Name, tools[i].Name())
		}
	}
}

func TestToolMap(t *testing.T) {
	tools := AllTools()
	m := ToolMap(tools)
	if len(m) != len(tools) {
		t.Errorf("expected %d entries, got %d", len(tools), len(m))
	}
	for _, tool := range tools {
		if got, ok := m[tool.Name()]; !ok || got.Name() != tool.Name() {
			t.Errorf("ToolMap missing or wrong entry for %q", tool.Name())
		}
	}
}
