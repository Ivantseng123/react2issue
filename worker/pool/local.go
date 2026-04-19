package pool

import (
	"context"
	"log/slog"
	"time"

	ghclient "github.com/Ivantseng123/agentdock/shared/github"
	"github.com/Ivantseng123/agentdock/shared/queue"
	"github.com/Ivantseng123/agentdock/worker/agent"
	"github.com/Ivantseng123/agentdock/worker/config"
)

// LocalAdapterConfig holds agent-specific configuration for the local adapter.
type LocalAdapterConfig struct {
	Runner         Runner
	RepoCache      RepoProvider
	SkillDirs      []string
	WorkerCount    int
	StatusInterval time.Duration
	Capabilities   []string
	Store          queue.JobStore
	Logger         *slog.Logger
	ExtraRules     []string
}

// LocalAdapter runs agents locally via Pool.
// It implements queue.Adapter.
type LocalAdapter struct {
	cfg  LocalAdapterConfig
	pool *Pool
}

func NewLocalAdapter(cfg LocalAdapterConfig) *LocalAdapter {
	return &LocalAdapter{cfg: cfg}
}

func (a *LocalAdapter) Name() string           { return "local" }
func (a *LocalAdapter) Capabilities() []string { return a.cfg.Capabilities }

func (a *LocalAdapter) Start(deps queue.AdapterDeps) error {
	a.pool = NewPool(Config{
		Queue:          deps.Jobs,
		Attachments:    deps.Attachments,
		Results:        deps.Results,
		Store:          a.cfg.Store,
		Runner:         a.cfg.Runner,
		RepoCache:      a.cfg.RepoCache,
		WorkerCount:    a.cfg.WorkerCount,
		SkillDirs:      a.cfg.SkillDirs,
		Commands:       deps.Commands,
		Status:         deps.Status,
		StatusInterval: a.cfg.StatusInterval,
		Logger:         a.cfg.Logger,
		ExtraRules:     a.cfg.ExtraRules,
	})
	a.pool.Start(context.Background())
	return nil
}

func (a *LocalAdapter) Stop() error {
	return nil
}

// StartLocal wires a LocalAdapter using a worker config and app-owned buses.
// Used by cmd/agentdock in inmem mode to co-locate worker execution with
// app processing while still letting worker.yaml control count + prompt.
//
// The caller (cmd layer) owns the buses (they come from app.Run) and the
// JobStore; StartLocal only reads worker-scope fields (agents, providers,
// count, prompt.extra_rules, repo_cache, queue.status_interval).
func StartLocal(cfg *config.Config, deps queue.AdapterDeps, store queue.JobStore, githubLogger, workerLogger *slog.Logger) (*LocalAdapter, error) {
	agentRunner := agent.NewRunnerFromConfig(cfg)
	repoCache := ghclient.NewRepoCache(cfg.RepoCache.Dir, cfg.RepoCache.MaxAge, cfg.GitHub.Token, githubLogger)

	var skillDirs []string
	seen := make(map[string]bool)
	for _, name := range cfg.Providers {
		if ag, ok := cfg.Agents[name]; ok && ag.SkillDir != "" && !seen[ag.SkillDir] {
			skillDirs = append(skillDirs, ag.SkillDir)
			seen[ag.SkillDir] = true
		}
	}
	if len(skillDirs) == 0 && cfg.ActiveAgent != "" {
		if ag, ok := cfg.Agents[cfg.ActiveAgent]; ok && ag.SkillDir != "" {
			skillDirs = append(skillDirs, ag.SkillDir)
		}
	}

	adapter := NewLocalAdapter(LocalAdapterConfig{
		Runner:         &AgentRunnerAdapter{Runner: agentRunner},
		RepoCache:      &RepoCacheAdapter{Cache: repoCache},
		SkillDirs:      skillDirs,
		WorkerCount:    cfg.Count,
		StatusInterval: cfg.Queue.StatusInterval,
		Capabilities:   []string{"triage"},
		Store:          store,
		Logger:         workerLogger,
		ExtraRules:     cfg.Prompt.ExtraRules,
	})
	if err := adapter.Start(deps); err != nil {
		return nil, err
	}
	return adapter, nil
}
