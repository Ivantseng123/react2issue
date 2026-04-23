package github

import (
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRepoCache_EnsureRepo_ClonesNewRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}

	bareDir := t.TempDir()
	run(t, bareDir, "git", "init", "--bare")

	workDir := t.TempDir()
	run(t, workDir, "git", "clone", bareDir, ".")
	os.WriteFile(filepath.Join(workDir, "main.go"), []byte("package main"), 0644)
	run(t, workDir, "git", "add", ".")
	run(t, workDir, "git", "-c", "user.name=test", "-c", "user.email=test@test.com", "commit", "-m", "init")
	run(t, workDir, "git", "push")

	cacheDir := t.TempDir()
	cache := NewRepoCache(cacheDir, time.Hour, "", slog.Default())

	repoPath, err := cache.EnsureRepo("file://"+bareDir, "")
	if err != nil {
		t.Fatalf("EnsureRepo failed: %v", err)
	}

	// Bare repo — verify HEAD exists, not working files.
	if _, err := os.Stat(filepath.Join(repoPath, "HEAD")); err != nil {
		t.Fatalf("bare repo missing HEAD: %v", err)
	}
}

func TestRepoCache_EnsureRepo_PullsExistingRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}

	bareDir := t.TempDir()
	run(t, bareDir, "git", "init", "--bare")

	workDir := t.TempDir()
	run(t, workDir, "git", "clone", bareDir, ".")
	os.WriteFile(filepath.Join(workDir, "main.go"), []byte("v1"), 0644)
	run(t, workDir, "git", "add", ".")
	run(t, workDir, "git", "-c", "user.name=test", "-c", "user.email=test@test.com", "commit", "-m", "v1")
	run(t, workDir, "git", "push")

	cacheDir := t.TempDir()
	cache := NewRepoCache(cacheDir, 0, "", slog.Default())

	repoPath, err := cache.EnsureRepo("file://"+bareDir, "")
	if err != nil {
		t.Fatalf("first EnsureRepo failed: %v", err)
	}

	os.WriteFile(filepath.Join(workDir, "main.go"), []byte("v2"), 0644)
	run(t, workDir, "git", "add", ".")
	run(t, workDir, "git", "-c", "user.name=test", "-c", "user.email=test@test.com", "commit", "-m", "v2")
	run(t, workDir, "git", "push")

	repoPath2, err := cache.EnsureRepo("file://"+bareDir, "")
	if err != nil {
		t.Fatalf("second EnsureRepo failed: %v", err)
	}
	if repoPath != repoPath2 {
		t.Error("expected same path for cached repo")
	}
}

func TestSanitizeURL(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"https://ghp_abc123@github.com/org/repo.git", "https://***@github.com/org/repo.git"},
		{"https://user:pass@github.com/org/repo.git", "https://***@github.com/org/repo.git"},
		{"https://github.com/org/repo.git", "https://github.com/org/repo.git"},
		{"git@github.com:org/repo.git", "git@github.com:org/repo.git"},
		{"file:///tmp/repo", "file:///tmp/repo"},
		{"org/repo", "org/repo"},
	}
	for _, tt := range tests {
		if got := SanitizeURL(tt.input); got != tt.want {
			t.Errorf("SanitizeURL(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestRepoCache_BareCloneAndWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}

	// Create a source bare repo with one commit.
	sourceDir := t.TempDir()
	run(t, sourceDir, "git", "init", "--bare")

	workDir := t.TempDir()
	run(t, workDir, "git", "clone", sourceDir, ".")
	os.WriteFile(filepath.Join(workDir, "main.go"), []byte("package main"), 0644)
	run(t, workDir, "git", "add", ".")
	run(t, workDir, "git", "-c", "user.name=test", "-c", "user.email=test@test.com", "commit", "-m", "init")
	run(t, workDir, "git", "push")

	cacheDir := t.TempDir()
	cache := NewRepoCache(cacheDir, time.Hour, "", slog.Default())

	// EnsureRepo should create a bare clone.
	barePath, err := cache.EnsureRepo("file://"+sourceDir, "")
	if err != nil {
		t.Fatalf("EnsureRepo failed: %v", err)
	}

	// Bare repo should NOT have working tree files.
	if _, err := os.Stat(filepath.Join(barePath, "main.go")); !os.IsNotExist(err) {
		t.Error("bare repo should not have working tree files")
	}
	// But should have HEAD (bare repo indicator).
	if _, err := os.Stat(filepath.Join(barePath, "HEAD")); err != nil {
		t.Errorf("bare repo missing HEAD: %v", err)
	}

	// AddWorktree should create a working directory.
	wtPath := filepath.Join(t.TempDir(), "wt1")
	if err := cache.AddWorktree(barePath, "", wtPath); err != nil {
		t.Fatalf("AddWorktree failed: %v", err)
	}

	content, err := os.ReadFile(filepath.Join(wtPath, "main.go"))
	if err != nil {
		t.Fatalf("worktree file not found: %v", err)
	}
	if string(content) != "package main" {
		t.Errorf("unexpected content: %s", string(content))
	}

	// RemoveWorktree should delete it.
	if err := cache.RemoveWorktree(wtPath); err != nil {
		t.Fatalf("RemoveWorktree failed: %v", err)
	}
	if _, err := os.Stat(wtPath); !os.IsNotExist(err) {
		t.Error("worktree dir should be deleted")
	}
}

func TestRepoCache_AddWorktree_SameBranchTwice(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}

	sourceDir := t.TempDir()
	run(t, sourceDir, "git", "init", "--bare")

	workDir := t.TempDir()
	run(t, workDir, "git", "clone", sourceDir, ".")
	os.WriteFile(filepath.Join(workDir, "main.go"), []byte("package main"), 0644)
	run(t, workDir, "git", "add", ".")
	run(t, workDir, "git", "-c", "user.name=test", "-c", "user.email=test@test.com", "commit", "-m", "init")
	run(t, workDir, "git", "push")

	cacheDir := t.TempDir()
	cache := NewRepoCache(cacheDir, time.Hour, "", slog.Default())

	barePath, err := cache.EnsureRepo("file://"+sourceDir, "")
	if err != nil {
		t.Fatalf("EnsureRepo failed: %v", err)
	}

	// Discover the branch name the source repo actually used (main vs master).
	out, err := exec.Command("git", "-C", barePath, "symbolic-ref", "--short", "HEAD").Output()
	if err != nil {
		t.Fatalf("read HEAD: %v", err)
	}
	branch := strings.TrimSpace(string(out))

	wt1 := filepath.Join(t.TempDir(), "wt1")
	if err := cache.AddWorktree(barePath, branch, wt1); err != nil {
		t.Fatalf("first AddWorktree(%q) failed: %v", branch, err)
	}
	wt2 := filepath.Join(t.TempDir(), "wt2")
	if err := cache.AddWorktree(barePath, branch, wt2); err != nil {
		t.Fatalf("second AddWorktree(%q) failed: %v", branch, err)
	}
}

func TestRepoCache_AddWorktree_PrunesStaleAdminRecord(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}

	sourceDir := t.TempDir()
	run(t, sourceDir, "git", "init", "--bare")

	workDir := t.TempDir()
	run(t, workDir, "git", "clone", sourceDir, ".")
	os.WriteFile(filepath.Join(workDir, "main.go"), []byte("package main"), 0644)
	run(t, workDir, "git", "add", ".")
	run(t, workDir, "git", "-c", "user.name=test", "-c", "user.email=test@test.com", "commit", "-m", "init")
	run(t, workDir, "git", "push")

	cacheDir := t.TempDir()
	cache := NewRepoCache(cacheDir, time.Hour, "", slog.Default())
	barePath, err := cache.EnsureRepo("file://"+sourceDir, "")
	if err != nil {
		t.Fatalf("EnsureRepo failed: %v", err)
	}

	// Simulate a crashed worker: create worktree, then rm working dir but
	// leave the admin record at <bare>/worktrees/NAME behind.
	doomed := filepath.Join(t.TempDir(), "doomed")
	if err := cache.AddWorktree(barePath, "", doomed); err != nil {
		t.Fatalf("AddWorktree(doomed) failed: %v", err)
	}
	if err := os.RemoveAll(doomed); err != nil {
		t.Fatalf("rm doomed: %v", err)
	}
	entries, _ := os.ReadDir(filepath.Join(barePath, "worktrees"))
	if len(entries) == 0 {
		t.Fatal("expected leftover admin record for test setup")
	}

	fresh := filepath.Join(t.TempDir(), "fresh")
	if err := cache.AddWorktree(barePath, "", fresh); err != nil {
		t.Fatalf("AddWorktree(fresh) after stale admin: %v", err)
	}
}

func TestRepoCache_AddWorktree_RetriesAfterFetchOnUnknownRef(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}

	// Source: bare repo with a "feature" branch + main. The feature commit's
	// SHA is what we'll later try to worktree-add after the branch is gone.
	sourceDir := t.TempDir()
	run(t, sourceDir, "git", "init", "--bare")
	// Allow fetch-by-SHA against this remote; mirrors GitHub's
	// uploadpack.allowReachableSHA1InWant behaviour.
	run(t, sourceDir, "git", "config", "uploadpack.allowAnySHA1InWant", "true")

	workDir := t.TempDir()
	run(t, workDir, "git", "clone", sourceDir, ".")
	os.WriteFile(filepath.Join(workDir, "main.go"), []byte("package main"), 0644)
	run(t, workDir, "git", "add", ".")
	run(t, workDir, "git", "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-m", "init")
	run(t, workDir, "git", "push")
	run(t, workDir, "git", "checkout", "-b", "feature")
	os.WriteFile(filepath.Join(workDir, "feature.go"), []byte("package main"), 0644)
	run(t, workDir, "git", "add", ".")
	run(t, workDir, "git", "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-m", "add feature")
	run(t, workDir, "git", "push", "-u", "origin", "feature")

	// Capture feature SHA, then delete the feature branch from source. The
	// commit object stays reachable on the source server because Git keeps
	// objects until gc; allowAnySHA1InWant lets clients fetch them.
	shaOut, err := exec.Command("git", "-C", workDir, "rev-parse", "feature").Output()
	if err != nil {
		t.Fatalf("rev-parse feature: %v", err)
	}
	featureSHA := strings.TrimSpace(string(shaOut))
	run(t, sourceDir, "git", "update-ref", "-d", "refs/heads/feature")

	// Fresh cache: EnsureRepo only pulls refs reachable from refs/heads/*,
	// so the deleted feature branch's commit is NOT in the cache.
	cacheDir := t.TempDir()
	cache := NewRepoCache(cacheDir, time.Hour, "", slog.Default())
	barePath, err := cache.EnsureRepo("file://"+sourceDir, "")
	if err != nil {
		t.Fatalf("EnsureRepo failed: %v", err)
	}

	// Sanity-check: SHA is unreachable locally before AddWorktree.
	if out, err := exec.Command("git", "-C", barePath, "cat-file", "-t", featureSHA).CombinedOutput(); err == nil {
		t.Fatalf("test setup invariant broken: feature SHA %s already reachable in cache (%s)", featureSHA, out)
	}

	// AddWorktree by SHA should fetch-retry and succeed.
	wt := filepath.Join(t.TempDir(), "wt")
	if err := cache.AddWorktree(barePath, featureSHA, wt); err != nil {
		t.Fatalf("AddWorktree(%s) failed: %v", featureSHA, err)
	}

	// Worktree HEAD should be the feature SHA.
	headOut, err := exec.Command("git", "-C", wt, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("rev-parse HEAD in worktree: %v", err)
	}
	if got := strings.TrimSpace(string(headOut)); got != featureSHA {
		t.Errorf("worktree HEAD = %s, want %s", got, featureSHA)
	}
}

func TestRepoCache_AddWorktree_PropagatesErrorWhenFetchAlsoFails(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}

	// Source: bare repo with main only. Set allowAnySHA1InWant so the fetch
	// attempt is well-formed; failure must come from the SHA not existing
	// on the server, not from server-side policy.
	sourceDir := t.TempDir()
	run(t, sourceDir, "git", "init", "--bare")
	run(t, sourceDir, "git", "config", "uploadpack.allowAnySHA1InWant", "true")

	workDir := t.TempDir()
	run(t, workDir, "git", "clone", sourceDir, ".")
	os.WriteFile(filepath.Join(workDir, "main.go"), []byte("package main"), 0644)
	run(t, workDir, "git", "add", ".")
	run(t, workDir, "git", "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-m", "init")
	run(t, workDir, "git", "push")

	cacheDir := t.TempDir()
	cache := NewRepoCache(cacheDir, time.Hour, "", slog.Default())
	barePath, err := cache.EnsureRepo("file://"+sourceDir, "")
	if err != nil {
		t.Fatalf("EnsureRepo failed: %v", err)
	}

	bogusSHA := "deadbeef00000000000000000000000000000beef"
	wt := filepath.Join(t.TempDir(), "wt")
	err = cache.AddWorktree(barePath, bogusSHA, wt)
	if err == nil {
		t.Fatal("expected error when ref is unknown to both cache and remote, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "worktree add") {
		t.Errorf("error should mention worktree add: %q", msg)
	}
	if !strings.Contains(msg, "fetch") {
		t.Errorf("error should mention fetch fallback failure: %q", msg)
	}
}

func TestRepoCache_CleanAll(t *testing.T) {
	cacheDir := t.TempDir()
	cache := NewRepoCache(cacheDir, time.Hour, "", slog.Default())

	os.WriteFile(filepath.Join(cacheDir, "dummy"), []byte("x"), 0644)

	if err := cache.CleanAll(); err != nil {
		t.Fatalf("CleanAll failed: %v", err)
	}
	if _, err := os.Stat(cacheDir); !os.IsNotExist(err) {
		t.Error("cache dir should be deleted")
	}
}

func TestRepoCache_PurgeStale(t *testing.T) {
	cacheDir := t.TempDir()
	cache := NewRepoCache(cacheDir, time.Hour, "", slog.Default())

	os.WriteFile(filepath.Join(cacheDir, "leftover"), []byte("x"), 0644)

	if err := cache.PurgeStale(); err != nil {
		t.Fatalf("PurgeStale failed: %v", err)
	}
	entries, err := os.ReadDir(cacheDir)
	if err != nil {
		t.Fatalf("ReadDir failed: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected empty cache dir, got %d entries", len(entries))
	}
}

func TestRepoCache_ResolveURLWithToken_PerCall(t *testing.T) {
	dir := t.TempDir()
	rc := NewRepoCache(dir, 0, "", slog.Default())
	url := rc.resolveURLWithToken("owner/repo", "ghp_percall")
	if url != "https://ghp_percall@github.com/owner/repo.git" {
		t.Errorf("got %q", url)
	}
}

func TestRepoCache_ResolveURLWithToken_Fallback(t *testing.T) {
	dir := t.TempDir()
	rc := NewRepoCache(dir, 0, "ghp_default", slog.Default())
	url := rc.resolveURLWithToken("owner/repo", "")
	if url != "https://ghp_default@github.com/owner/repo.git" {
		t.Errorf("got %q", url)
	}
}

func TestRepoCache_ResolveURLWithToken_NoToken(t *testing.T) {
	dir := t.TempDir()
	rc := NewRepoCache(dir, 0, "", slog.Default())
	url := rc.resolveURLWithToken("owner/repo", "")
	if url != "https://github.com/owner/repo.git" {
		t.Errorf("got %q", url)
	}
}

func TestRepoCache_ResolveURLWithToken_FullGithubURLInjectsToken(t *testing.T) {
	dir := t.TempDir()
	rc := NewRepoCache(dir, 0, "", slog.Default())
	url := rc.resolveURLWithToken("https://github.com/owner/repo.git", "ghp_percall")
	if url != "https://ghp_percall@github.com/owner/repo.git" {
		t.Errorf("got %q", url)
	}
}

func TestRepoCache_ResolveURLWithToken_FullGithubURLFallbackPAT(t *testing.T) {
	dir := t.TempDir()
	rc := NewRepoCache(dir, 0, "ghp_default", slog.Default())
	url := rc.resolveURLWithToken("https://github.com/owner/repo.git", "")
	if url != "https://ghp_default@github.com/owner/repo.git" {
		t.Errorf("got %q", url)
	}
}

func TestRepoCache_ResolveURLWithToken_FullGithubURLNoTokenPassthrough(t *testing.T) {
	dir := t.TempDir()
	rc := NewRepoCache(dir, 0, "", slog.Default())
	url := rc.resolveURLWithToken("https://github.com/owner/repo.git", "")
	if url != "https://github.com/owner/repo.git" {
		t.Errorf("got %q", url)
	}
}

func TestRepoCache_ResolveURLWithToken_URLWithExistingUserinfoPassthrough(t *testing.T) {
	dir := t.TempDir()
	rc := NewRepoCache(dir, 0, "ghp_default", slog.Default())
	url := rc.resolveURLWithToken("https://ghp_old@github.com/owner/repo.git", "ghp_new")
	if url != "https://ghp_old@github.com/owner/repo.git" {
		t.Errorf("got %q", url)
	}
}

func TestRepoCache_ResolveURLWithToken_NonGithubHostPassthrough(t *testing.T) {
	dir := t.TempDir()
	rc := NewRepoCache(dir, 0, "ghp_default", slog.Default())
	url := rc.resolveURLWithToken("https://gitlab.com/owner/repo.git", "ghp_percall")
	if url != "https://gitlab.com/owner/repo.git" {
		t.Errorf("got %q", url)
	}
}

func TestRepoCache_ResolveURLWithToken_SSHAndFilePassthrough(t *testing.T) {
	dir := t.TempDir()
	rc := NewRepoCache(dir, 0, "ghp_default", slog.Default())
	if got := rc.resolveURLWithToken("git@github.com:owner/repo.git", "ghp_x"); got != "git@github.com:owner/repo.git" {
		t.Errorf("git@: got %q", got)
	}
	if got := rc.resolveURLWithToken("file:///tmp/repo", "ghp_x"); got != "file:///tmp/repo" {
		t.Errorf("file://: got %q", got)
	}
}

func run(t *testing.T, dir string, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v failed: %v\n%s", name, args, err, out)
	}
}
