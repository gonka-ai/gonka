package escrow

import (
	"context"
	"errors"
	"fmt"

	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/funding"
	"devshard/cmd/gateway/store"
)

func (m *Manager) applyPlan(ctx context.Context, model ModelConfig, snapshot chain.PhaseSnapshot, rows []store.DevshardRecord, decision funding.Decision, inBridgeWindow bool) error {
	var errs []error
	for _, planned := range decision.Retires {
		record, found := activeRow(rows, planned.EscrowID)
		if !found {
			continue
		}
		if err := m.retire(ctx, record); err != nil && !deferredRetire(err) {
			errs = append(errs, fmt.Errorf("retiring escrow %s for %s: %w", record.EscrowID, planned.Reason, err))
		}
	}
	for _, create := range decision.Creates {
		role := roleRegular
		if create.Standby {
			role = RoleReserve
		}
		_, err := m.createFor(ctx, model, role, snapshot.EpochIndex, string(create.Reason), snapshot)
		m.planner.recordCreate(model.ModelID, err, m.now())
		if err == nil {
			m.breaker.reset(model.ModelID, roleRegular)
			continue
		}
		if !refusedBeforeBroadcast(err) {
			m.breaker.recordFailure(model.ModelID, roleRegular)
			errs = append(errs, fmt.Errorf("planned %s create for %s: %w", create.Reason, model.ModelID, err))
		}
		break
	}
	if decision.Shortfalls.Spread == 0 && !inBridgeWindow && !snapshot.RequestsBlocked {
		errs = append(errs, m.retireBridgedTemps(ctx, snapshot, model, rows))
	}
	return errors.Join(errs...)
}

func (m *Manager) retireBridgedTemps(ctx context.Context, snapshot chain.PhaseSnapshot, model ModelConfig, rows []store.DevshardRecord) error {
	retired := 0
	var errs []error
	for _, record := range rows {
		if !isActiveTemp(record, model.ModelID, int64(snapshot.EpochIndex)) {
			continue
		}
		if err := m.retire(ctx, record); err != nil {
			if !deferredRetire(err) {
				errs = append(errs, fmt.Errorf("retiring bridged temp %s of %s: %w", record.EscrowID, model.ModelID, err))
			}
			continue
		}
		retired++
	}
	if retired > 0 && m.narrator != nil {
		m.narrator.BridgeFinished(model.ModelID, snapshot.EpochIndex, 0, retired)
	}
	return errors.Join(errs...)
}

func activeRow(rows []store.DevshardRecord, escrowID string) (store.DevshardRecord, bool) {
	for _, record := range rows {
		if record.EscrowID == escrowID && record.Active {
			return record, true
		}
	}
	return store.DevshardRecord{}, false
}
