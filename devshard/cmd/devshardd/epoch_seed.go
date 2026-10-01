package main

import (
	"context"
	"log/slog"
	"time"

	"common/chain"
)

type currentEpochSource interface {
	CurrentEpochID() uint64
}

// seedEpochWhenKnown covers a boot where neither bootstrapPhase nor the
// runtime-config snapshot had an epoch yet. The first real snapshot that
// arrives later is an initial apply and does not fire OnEpochChange, so
// without this phase stays 0 until the next epoch transition and every
// payload executed meanwhile is stored under epoch 0, where validators asking
// under the escrow epoch do not find it.
func seedEpochWhenKnown(ctx context.Context, src currentEpochSource, phase *chain.Phase, interval time.Duration, apply func(uint64)) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if phase.EpochID() > 0 {
			return // OnEpochChange got there first
		}
		if epoch := src.CurrentEpochID(); epoch > 0 {
			slog.Info("phase: seeded from first runtime-config snapshot", "epoch", epoch)
			apply(epoch)
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
