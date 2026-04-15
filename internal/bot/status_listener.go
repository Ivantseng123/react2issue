package bot

import (
	"context"
	"log/slog"

	"agentdock/internal/queue"
)

type StatusListener struct {
	status queue.StatusBus
	store  queue.JobStore
	logger *slog.Logger
}

func NewStatusListener(status queue.StatusBus, store queue.JobStore, logger *slog.Logger) *StatusListener {
	return &StatusListener{status: status, store: store, logger: logger}
}

func (l *StatusListener) Listen(ctx context.Context) {
	ch, err := l.status.Subscribe(ctx)
	if err != nil {
		l.logger.Error("訂閱狀態匯流排失敗", "phase", "失敗", "error", err)
		return
	}
	for {
		select {
		case report, ok := <-ch:
			if !ok {
				return
			}
			l.store.SetAgentStatus(report.JobID, report)
		case <-ctx.Done():
			return
		}
	}
}
