package worker

import (
	"context"
	"log/slog"
	"time"

	usecases "trainshard/internal/application/hostd/run/use_cases"
	"trainshard/internal/domain/shard"
	"trainshard/internal/domain/shared/vo"
)

type Reconciler struct {
	nodes     []vo.NodeRef
	reconcile usecases.Reconciler
	watcher   shard.ChainWatcher
	interval  time.Duration
	log       *slog.Logger
}

func NewReconciler(
	nodes []vo.NodeRef,
	reconcile usecases.Reconciler,
	watcher shard.ChainWatcher,
	interval time.Duration,
	log *slog.Logger,
) *Reconciler {
	return &Reconciler{nodes: nodes, reconcile: reconcile, watcher: watcher, interval: interval, log: log}
}

func (r *Reconciler) Run(ctx context.Context) {
	hints := r.hints(ctx)
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	for {
		r.tick(ctx)

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case _, open := <-hints:
			if !open {
				hints = nil
			}
		}
	}
}

func (r *Reconciler) tick(ctx context.Context) {
	for _, node := range r.nodes {
		if err := r.reconcile.Execute(ctx, node); err != nil && ctx.Err() == nil {
			r.log.ErrorContext(ctx, "reconcile failed", "node_id", node.NodeID, "error", err)
		}
	}
}

func (r *Reconciler) hints(ctx context.Context) <-chan struct{} {
	if r.watcher == nil {
		return nil
	}
	hints, err := r.watcher.Watch(ctx)
	if err != nil {
		r.log.WarnContext(ctx, "chain events unavailable, the ticker alone drives the loop", "error", err)
		return nil
	}
	return hints
}
