package worker

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"slack-issue-bot/internal/bot"
	"slack-issue-bot/internal/queue"
)

type Config struct {
	Queue       queue.JobQueue
	Attachments queue.AttachmentStore
	Results     queue.ResultBus
	Store       queue.JobStore
	Runner      Runner
	RepoCache   RepoProvider
	WorkerCount int
	SkillDir    string
}

type Pool struct {
	cfg Config
}

func NewPool(cfg Config) *Pool {
	return &Pool{cfg: cfg}
}

func (p *Pool) Start(ctx context.Context) {
	for i := 0; i < p.cfg.WorkerCount; i++ {
		go p.runWorker(ctx, i)
	}
	slog.Info("worker pool started", "count", p.cfg.WorkerCount)
}

func (p *Pool) runWorker(ctx context.Context, id int) {
	logger := slog.With("worker_id", id)
	jobs, err := p.cfg.Queue.Receive(ctx)
	if err != nil {
		logger.Error("failed to receive jobs", "error", err)
		return
	}

	deps := executionDeps{
		attachments: p.cfg.Attachments,
		repoCache:   p.cfg.RepoCache,
		runner:      p.cfg.Runner,
		store:       p.cfg.Store,
		skillDir:    p.cfg.SkillDir,
	}

	for {
		select {
		case job, ok := <-jobs:
			if !ok {
				logger.Info("job channel closed, worker exiting")
				return
			}
			logger.Info("received job", "job_id", job.ID, "repo", job.Repo)

			if err := p.cfg.Queue.Ack(ctx, job.ID); err != nil {
				logger.Error("ack failed", "job_id", job.ID, "error", err)
				result := failedResult(job, time.Now(), fmt.Errorf("ack failed: %w", err))
				p.cfg.Results.Publish(ctx, result)
				continue
			}

			result := executeJob(ctx, job, deps, bot.RunOptions{})
			p.cfg.Store.UpdateStatus(job.ID, queue.JobStatus(result.Status))
			if err := p.cfg.Results.Publish(ctx, result); err != nil {
				logger.Error("failed to publish result", "job_id", job.ID, "error", err)
			}
			logger.Info("job completed", "job_id", job.ID, "status", result.Status)
		case <-ctx.Done():
			logger.Info("worker shutting down")
			return
		}
	}
}
