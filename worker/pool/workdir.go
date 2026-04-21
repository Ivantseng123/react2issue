package pool

import (
	"fmt"
	"os"

	"github.com/Ivantseng123/agentdock/shared/queue"
)

// WorkDirProvider prepares and cleans up the working directory an agent
// runs against. Two implementations:
//   - RepoCloneProvider: wraps RepoProvider (clone + remove worktree)
//   - EmptyDirProvider: mkdir temp dir + RemoveAll
//
// executeJob selects between them based on whether the Job carries a CloneURL.
type WorkDirProvider interface {
	Prepare(job *queue.Job) (path string, err error)
	Cleanup(path string)
}

// RepoCloneProvider wraps the existing RepoProvider so jobs with CloneURL set
// continue to use the repo cache + git checkout path unchanged.
type RepoCloneProvider struct {
	Repo  RepoProvider
	Token string // github token
}

func (p *RepoCloneProvider) Prepare(job *queue.Job) (string, error) {
	return p.Repo.Prepare(job.CloneURL, job.Branch, p.Token)
}

func (p *RepoCloneProvider) Cleanup(path string) {
	if path == "" {
		return
	}
	if err := p.Repo.RemoveWorktree(path); err != nil {
		// best-effort
	}
}

// EmptyDirProvider mkdirs a fresh temp directory per job. Used by Ask when
// the user didn't attach a repo.
type EmptyDirProvider struct{}

func (p *EmptyDirProvider) Prepare(job *queue.Job) (string, error) {
	dir, err := os.MkdirTemp("", fmt.Sprintf("ask-%s-*", job.ID))
	if err != nil {
		return "", fmt.Errorf("mkdir temp workdir: %w", err)
	}
	return dir, nil
}

func (p *EmptyDirProvider) Cleanup(path string) {
	if path == "" {
		return
	}
	_ = os.RemoveAll(path)
}

// selectProvider picks between RepoCloneProvider and EmptyDirProvider based
// on whether the Job has a CloneURL. The choice is by CloneURL rather than by
// TaskType to keep worker fully workflow-agnostic (spec Goal #6).
func selectProvider(job *queue.Job, repo RepoProvider, token string) WorkDirProvider {
	if job.CloneURL == "" {
		return &EmptyDirProvider{}
	}
	return &RepoCloneProvider{Repo: repo, Token: token}
}
