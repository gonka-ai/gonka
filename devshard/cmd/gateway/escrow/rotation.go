package escrow

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/store"
)

// errCreateSuppressed marks a create the breaker refused -- not "nothing needed". See README.md, "The bridge across proof-of-compute".
var errCreateSuppressed = errors.New("escrow creation suppressed by the create breaker")

var errBridgePartialFill = errors.New("bridge funded fewer temps than temp_count")

// rotationFill is one model's share of a fill: the creates it wants, the ones that landed, and the error that stopped it.
type rotationFill struct {
	model   ModelConfig
	wanted  int
	created int
	err     error
}

func (m *Manager) fillToLabel(ctx context.Context, role string, label uint64, models []ModelConfig, targetOf func(ModelConfig) int, snapshot chain.PhaseSnapshot, devshards []store.DevshardRecord) []rotationFill {
	fills := make([]rotationFill, len(models))
	for index, model := range models {
		wanted, err := m.createsWanted(role, label, targetOf(model), model, snapshot, devshards)
		fills[index] = rotationFill{model: model, wanted: wanted, err: err}
	}
	for creating := true; creating; {
		creating = false
		for index := range fills {
			fill := &fills[index]
			if fill.err != nil || fill.created >= fill.wanted {
				continue
			}
			if _, err := m.createFor(ctx, fill.model, role, label, string(createdForBridge), snapshot); err != nil {
				if !refusedBeforeBroadcast(err) {
					m.breaker.recordFailure(fill.model.ModelID, role)
				}
				fill.err = err
				continue
			}
			fill.created++
			creating = true
		}
	}
	return fills
}

func (m *Manager) createsWanted(role string, label uint64, target int, model ModelConfig, snapshot chain.PhaseSnapshot, devshards []store.DevshardRecord) (int, error) {
	existing := countActive(devshards, model.ModelID, role, int64(label))
	if existing >= target {
		return 0, nil
	}
	if served, known := servedByNetwork(snapshot, model.ModelID); known && !served {
		if m.narrator != nil {
			m.narrator.RotationSkipped(model.ModelID, role, snapshot.EpochIndex)
		}
		return 0, nil
	}
	if m.breaker.gated(model.ModelID, role) {
		return 0, errCreateSuppressed
	}
	return target - existing, nil
}

func countActive(devshards []store.DevshardRecord, modelID, role string, epoch int64) int {
	count := 0
	for _, record := range devshards {
		if record.Active && record.RotationRole == role && record.RotationEpoch == epoch && record.Model == modelID {
			count++
		}
	}
	return count
}

// prepareBridge swaps temp escrows in ahead of an epoch switch, one model at a time, failure-isolated.
func (m *Manager) prepareBridge(ctx context.Context, snapshot chain.PhaseSnapshot, models []ModelConfig, devshards []store.DevshardRecord) error {
	label := bridgeLabel(snapshot)
	room, err := m.bridgeRoom(ctx, models, devshards)
	if err != nil {
		return fmt.Errorf("prepare bridge: %w", err)
	}
	targetOf := func(model ModelConfig) int {
		limit, capped := room[model.ModelID]
		if !capped {
			return model.TempCount
		}
		return min(model.TempCount, countActive(devshards, model.ModelID, roleTemp, int64(label))+max(limit, 0))
	}
	var errs []error
	for _, fill := range m.fillToLabel(ctx, roleTemp, label, models, targetOf, snapshot, devshards) {
		model, created, err := fill.model, fill.created, fill.err
		if funded := countActive(devshards, model.ModelID, roleTemp, int64(label)) + created; err == nil && funded < model.TempCount {
			err = fmt.Errorf("%w: %d of %d for %s", errBridgePartialFill, funded, model.TempCount, model.ModelID)
		}
		if err != nil {
			// degrade, don't abort: relabel existing regulars as temp so this epoch still has bridge coverage.
			promoted, promoteErr := m.promoteRegularsToTemp(ctx, model, devshards)
			if promoted > 0 && m.narrator != nil {
				m.narrator.RegularsPromotedToTemp(model.ModelID, snapshot.EpochIndex, promoted)
			}
			if promoteErr != nil {
				errs = append(errs, promoteErr)
			}
			status := store.RotationStatus{Model: model.ModelID, Role: roleTemp, Stage: stagePrepareTemp, Epoch: snapshot.EpochIndex, CreateError: err.Error()}
			if saveErr := m.saveRotationStatus(ctx, status); saveErr != nil {
				errs = append(errs, saveErr)
			}
			if !refusedBeforeBroadcast(err) && !errors.Is(err, errBridgePartialFill) {
				errs = append(errs, fmt.Errorf("prepare bridge for %s: %w", model.ModelID, err))
			}
			continue
		}

		retired, settleFailed := 0, 0
		for _, record := range devshards {
			if isActiveNonTemp(record, model.ModelID) {
				if err := m.retire(ctx, record); err != nil {
					settleFailed++
					if !deferredRetire(err) {
						errs = append(errs, fmt.Errorf("retiring escrow %s while bridging %s: %w", record.EscrowID, model.ModelID, err))
					}
					continue
				}
				retired++
			}
		}
		if (created > 0 || retired > 0) && m.narrator != nil {
			m.narrator.BridgePrepared(model.ModelID, snapshot.EpochIndex, created, retired)
		}
		status := store.RotationStatus{Model: model.ModelID, Role: roleTemp, Stage: stagePrepareTemp, Epoch: snapshot.EpochIndex, Completed: settleFailed == 0}
		if err := m.saveRotationStatus(ctx, status); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (m *Manager) bridgeRoom(ctx context.Context, models []ModelConfig, devshards []store.DevshardRecord) (map[string]int, error) {
	room := map[string]int{}
	if !slices.ContainsFunc(models, func(model ModelConfig) bool { return model.MaxUnsettled > 0 }) {
		return room, nil
	}
	commitments, err := m.store.LoadCommitments(ctx)
	if err != nil {
		return nil, err
	}
	for _, model := range models {
		if model.MaxUnsettled > 0 {
			room[model.ModelID] = model.MaxUnsettled - unsettledCount(model.ModelID, devshards, commitments)
		}
	}
	return room, nil
}

func isActiveTemp(record store.DevshardRecord, modelID string, epoch int64) bool {
	return record.Active && record.RotationRole == roleTemp && record.Model == modelID && record.RotationEpoch <= epoch
}

// isActiveNonTemp is any active escrow not already in the temp role, a reserve included, whatever epoch it was created under.
func isActiveNonTemp(record store.DevshardRecord, modelID string) bool {
	return record.Active && record.RotationRole != roleTemp && record.Model == modelID
}

// deferredRetire reports the outcomes that mean "not yet" rather than "failed"; anything else must reach the tick.
func deferredRetire(err error) bool {
	return errors.Is(err, ErrDevshardBusy) || errors.Is(err, ErrSettlementInFlight)
}

func refusedBeforeBroadcast(err error) bool {
	return errors.Is(err, chain.ErrWalletUnderfunded) || errors.Is(err, ErrAmountBelowFloor)
}

// The prepareBridge degrade path: it relabels regulars in place, and keeps going past a write failure.
func (m *Manager) promoteRegularsToTemp(ctx context.Context, model ModelConfig, devshards []store.DevshardRecord) (promoted int, err error) {
	for _, record := range devshards {
		if !isActiveNonTemp(record, model.ModelID) || record.RotationRole == RoleReserve {
			continue
		}
		if writeErr := m.store.WithRetry(ctx, func() error {
			return m.store.SetDevshardRotationRole(ctx, record.EscrowID, roleTemp)
		}); writeErr != nil {
			if err == nil {
				err = fmt.Errorf("promoting escrow %s to temp: %w", record.EscrowID, writeErr)
			}
			continue
		}
		promoted++
	}
	return promoted, err
}

func (m *Manager) saveRotationStatus(ctx context.Context, s store.RotationStatus) error {
	s.UpdatedAt = m.now()
	return m.store.WithRetry(ctx, func() error { return m.store.SaveRotationStatus(ctx, s) })
}
