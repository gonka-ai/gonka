package escrow

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/store"
)

// OnReserveTaken is called from the request path: it marks and wakes the tick, and does no I/O.
func (m *Manager) OnReserveTaken(escrowID string) {
	if !m.reserveTaken.mark(escrowID) {
		return
	}
	if m.narrator != nil {
		m.narrator.EscrowReserveTaken(escrowID)
	}
	select {
	case m.wakeup <- struct{}{}:
	default:
	}
}

// promoteTakenReserves rewrites a taken reserve's row as regular before the planner reads it; a failed write is marked again for the next tick.
func (m *Manager) promoteTakenReserves(ctx context.Context, devshards []store.DevshardRecord) ([]store.DevshardRecord, error) {
	taken := m.reserveTaken.drain()
	if len(taken) == 0 {
		return devshards, nil
	}
	promoted := slices.Clone(devshards)
	var errs []error
	for index, record := range promoted {
		if !taken[record.EscrowID] || record.RotationRole != RoleReserve {
			continue
		}
		if err := m.store.WithRetry(ctx, func() error {
			return m.store.SetDevshardRotationRole(ctx, record.EscrowID, roleRegular)
		}); err != nil {
			m.reserveTaken.mark(record.EscrowID)
			errs = append(errs, fmt.Errorf("promoting reserve escrow %s: %w", record.EscrowID, err))
			continue
		}
		promoted[index].RotationRole = roleRegular
	}
	return promoted, errors.Join(errs...)
}

// retireSurplusReserves settles a reserve no longer wanted: one an earlier epoch funded that the bridge did not retire, or one past a lowered reserve_count. A reserve carries no traffic, so its money comes back at once.
func (m *Manager) retireSurplusReserves(ctx context.Context, snapshot chain.PhaseSnapshot, models []ModelConfig, devshards []store.DevshardRecord) error {
	wanted := make(map[string]int, len(models))
	for _, model := range models {
		wanted[model.ModelID] = model.ReserveCount
	}
	kept := map[string]int{}
	var errs []error
	for _, record := range devshards {
		if !record.Active || record.RotationRole != RoleReserve {
			continue
		}
		if record.RotationEpoch >= int64(snapshot.EpochIndex) && kept[record.Model] < wanted[record.Model] {
			kept[record.Model]++
			continue
		}
		if err := m.retire(ctx, record); err != nil && !deferredRetire(err) {
			errs = append(errs, fmt.Errorf("retiring surplus reserve escrow %s: %w", record.EscrowID, err))
		}
	}
	return errors.Join(errs...)
}
