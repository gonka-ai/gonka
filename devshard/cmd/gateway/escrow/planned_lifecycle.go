package escrow

import (
	"context"
	"errors"
	"fmt"

	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/config"
	"devshard/cmd/gateway/scheduler"
	"devshard/cmd/gateway/store"
)

func (m *Manager) runPlannedLifecycle(ctx context.Context, snapshot chain.PhaseSnapshot, models []ModelConfig, devshards []store.DevshardRecord, rotation config.Rotation) error {
	promoted, promoteErr := m.promoteTakenReserves(ctx, devshards)
	m.markSpent(promoted)
	lifecycleErr := errors.Join(promoteErr, m.drainPlannedMarks(ctx, promoted))
	if !rotation.Enabled || len(models) == 0 || snapshotHasNoEpochYet(snapshot) {
		return lifecycleErr
	}
	blocksToEpochSwitch := snapshot.EpochSwitchBlockHeight - snapshot.BlockHeight
	if blocksToEpochSwitch >= 0 && blocksToEpochSwitch <= rotation.PrePoCBlocks {
		return errors.Join(lifecycleErr, m.prepareBridge(ctx, snapshot, models, promoted))
	}
	if snapshot.RequestsBlocked {
		return lifecycleErr
	}
	return errors.Join(lifecycleErr, m.retireSurplusReserves(ctx, snapshot, models, promoted))
}

func (m *Manager) drainPlannedMarks(ctx context.Context, devshards []store.DevshardRecord) error {
	marked := m.depleted.drain()
	if len(marked) == 0 {
		return nil
	}
	var errs []error
	for _, record := range devshards {
		reason, isMarked := marked[record.EscrowID]
		if !record.Active || !isMarked {
			continue
		}
		if reason != scheduler.ExhaustionNonceCap {
			m.planner.ignoreMark(record.Model, string(reason), m.now())
			continue
		}
		if err := m.retire(retiringAtNonceCap(ctx), record); err != nil && !deferredRetire(err) {
			errs = append(errs, fmt.Errorf("retiring nonce-capped escrow %s: %w", record.EscrowID, err))
		}
	}
	return errors.Join(errs...)
}
