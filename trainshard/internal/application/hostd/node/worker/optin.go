package worker

import (
	"context"
	"log/slog"
	"time"

	usecases "trainshard/internal/application/hostd/node/use_cases"
	"trainshard/internal/domain/shared/vo"
)

type OptIn struct {
	nodes    []vo.NodeRef
	refresh  *usecases.RefreshOptInUseCase
	interval time.Duration
	log      *slog.Logger
}

func NewOptIn(nodes []vo.NodeRef, refresh *usecases.RefreshOptInUseCase, interval time.Duration, log *slog.Logger) *OptIn {
	return &OptIn{nodes: nodes, refresh: refresh, interval: interval, log: log}
}

func (o *OptIn) Run(ctx context.Context) {
	ticker := time.NewTicker(o.interval)
	defer ticker.Stop()

	for {
		o.tick(ctx)

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (o *OptIn) tick(ctx context.Context) {
	for _, node := range o.nodes {
		result, err := o.refresh.Execute(ctx, node)
		switch {
		case err != nil && ctx.Err() == nil:
			o.log.ErrorContext(ctx, "opt-in refresh failed", "node_id", node.NodeID, "error", err)
		case !result.Ready:
			o.log.WarnContext(ctx, "node is not ready, opt-in not refreshed", "node_id", node.NodeID, "reason", result.Reason())
		}
	}
}
