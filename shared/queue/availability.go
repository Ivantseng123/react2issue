package queue

import (
	"context"
	"log/slog"
	"time"
)

type VerdictKind string

const (
	VerdictOK            VerdictKind = "ok"
	VerdictBusyEnqueueOK VerdictKind = "busy_enqueue"
	VerdictNoWorkers     VerdictKind = "no_workers"
)

type Verdict struct {
	Kind          VerdictKind
	WorkerCount   int
	ActiveJobs    int
	TotalSlots    int
	EstimatedWait time.Duration
}

type WorkerAvailability interface {
	CheckSoft(ctx context.Context) Verdict
	CheckHard(ctx context.Context) Verdict
}

type AvailabilityConfig struct {
	AvgJobDuration time.Duration
}

type availability struct {
	queue  JobQueue
	store  JobStore
	avgJob time.Duration
	logger *slog.Logger
}

func NewWorkerAvailability(q JobQueue, store JobStore, cfg AvailabilityConfig) WorkerAvailability {
	avg := cfg.AvgJobDuration
	if avg <= 0 {
		avg = 3 * time.Minute
	}
	return &availability{
		queue:  q,
		store:  store,
		avgJob: avg,
		logger: slog.Default(),
	}
}

func (a *availability) CheckSoft(ctx context.Context) Verdict { return a.compute(ctx) }
func (a *availability) CheckHard(ctx context.Context) Verdict { return a.compute(ctx) }

func (a *availability) compute(ctx context.Context) Verdict {
	workers, err := a.queue.ListWorkers(ctx)
	if err != nil {
		a.logger.Warn("可用性檢查: 列舉 worker 失敗", "phase", "失敗", "error", err)
		return Verdict{Kind: VerdictOK}
	}
	totalSlots := 0
	for _, w := range workers {
		totalSlots += normaliseSlots(w.Slots)
	}
	if len(workers) == 0 {
		return Verdict{Kind: VerdictNoWorkers}
	}
	return Verdict{
		Kind:        VerdictOK,
		WorkerCount: len(workers),
		TotalSlots:  totalSlots,
	}
}

func normaliseSlots(s int) int {
	if s <= 0 {
		return 1
	}
	return s
}
