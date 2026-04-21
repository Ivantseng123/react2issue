package pool

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Ivantseng123/agentdock/shared/queue"
)

func TestEmptyDirProvider_PrepareAndCleanup(t *testing.T) {
	p := &EmptyDirProvider{}
	job := &queue.Job{ID: "j1"}

	dir, err := p.Prepare(job)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if dir == "" {
		t.Fatal("empty dir path")
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("expected dir to exist: %v", err)
	}

	// Writable?
	if err := os.WriteFile(filepath.Join(dir, "marker"), []byte("x"), 0644); err != nil {
		t.Errorf("dir not writable: %v", err)
	}

	p.Cleanup(dir)
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("Cleanup did not remove dir")
	}
}

func TestSelectProvider_ChoosesByCloneURL(t *testing.T) {
	tests := []struct {
		name     string
		cloneURL string
		wantKind string
	}{
		{"empty clone URL → EmptyDirProvider", "", "empty"},
		{"repo URL → RepoCloneProvider", "https://github.com/foo/bar.git", "clone"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo := &fakeRepoProvider{}
			got := selectProvider(&queue.Job{CloneURL: tc.cloneURL}, repo, "")
			switch tc.wantKind {
			case "empty":
				if _, ok := got.(*EmptyDirProvider); !ok {
					t.Errorf("want *EmptyDirProvider, got %T", got)
				}
			case "clone":
				if _, ok := got.(*RepoCloneProvider); !ok {
					t.Errorf("want *RepoCloneProvider, got %T", got)
				}
			}
		})
	}
}

type fakeRepoProvider struct {
	prepared struct {
		cloneURL string
		branch   string
		token    string
	}
	cleaned string
}

func (f *fakeRepoProvider) Prepare(cloneURL, branch, token string) (string, error) {
	f.prepared.cloneURL = cloneURL
	f.prepared.branch = branch
	f.prepared.token = token
	return "/tmp/fake-repo", nil
}
func (f *fakeRepoProvider) RemoveWorktree(p string) error { f.cleaned = p; return nil }
func (f *fakeRepoProvider) CleanAll() error               { return nil }
func (f *fakeRepoProvider) PurgeStale() error             { return nil }

func TestRepoCloneProvider_ForwardsArgs(t *testing.T) {
	fake := &fakeRepoProvider{}
	provider := &RepoCloneProvider{Repo: fake, Token: "tkn"}
	job := &queue.Job{CloneURL: "https://github.com/foo/bar.git", Branch: "main"}

	got, err := provider.Prepare(job)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if got != "/tmp/fake-repo" {
		t.Errorf("path = %q, want /tmp/fake-repo", got)
	}
	if fake.prepared.cloneURL != "https://github.com/foo/bar.git" {
		t.Errorf("cloneURL = %q", fake.prepared.cloneURL)
	}
	if fake.prepared.branch != "main" {
		t.Errorf("branch = %q", fake.prepared.branch)
	}
	if fake.prepared.token != "tkn" {
		t.Errorf("token = %q", fake.prepared.token)
	}

	provider.Cleanup(got)
	if fake.cleaned != "/tmp/fake-repo" {
		t.Errorf("cleaned = %q, want /tmp/fake-repo", fake.cleaned)
	}
}
