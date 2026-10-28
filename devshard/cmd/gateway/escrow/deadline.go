package escrow

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"

	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/store"
)

// settleDeadline is where an escrow stands against the last block its chain epoch can still settle in. See README.md, "Settlement by deadline".
type settleDeadline struct {
	known    bool
	passed   bool
	inMargin bool
	settleBy int64
}

func (deadline settleDeadline) order() int64 {
	switch {
	case deadline.passed:
		return math.MinInt64
	case deadline.settleBy > 0:
		return deadline.settleBy
	}
	return math.MaxInt64
}

// chainEpochOf falls back to the row label less one, the lower bound, while the chain epoch is unresolved.
func chainEpochOf(record store.DevshardRecord) (uint64, bool) {
	switch {
	case record.ChainEpoch > 0:
		return record.ChainEpoch, true
	case record.RotationEpoch > 1:
		return uint64(record.RotationEpoch - 1), true
	}
	return 0, false
}

func deadlineAt(record store.DevshardRecord, snapshot chain.PhaseSnapshot, marginBlocks int64) settleDeadline {
	epoch, known := chainEpochOf(record)
	if !known {
		return settleDeadline{}
	}
	return deadlineOfEpoch(epoch, snapshot, marginBlocks)
}

func deadlineOfEpoch(epoch uint64, snapshot chain.PhaseSnapshot, marginBlocks int64) settleDeadline {
	if snapshotHasNoEpochYet(snapshot) {
		return settleDeadline{}
	}
	effective := effectiveEpoch(snapshot)
	switch {
	case effective >= epoch+2:
		return settleDeadline{known: true, passed: true, inMargin: true}
	case effective == epoch+1:
		settleBy := snapshot.EpochSwitchBlockHeight
		return settleDeadline{known: true, settleBy: settleBy, inMargin: settleBy <= 0 || snapshot.BlockHeight >= settleBy-marginBlocks}
	}
	return settleDeadline{known: true}
}

func (m *Manager) deadlineOf(record store.DevshardRecord, snapshot chain.PhaseSnapshot) settleDeadline {
	if m.chainFacts == nil {
		return settleDeadline{}
	}
	return deadlineAt(record, snapshot, m.config.Load().Rotation.SettleMarginBlocks)
}

// parkAtDeadline runs whatever the rotation toggle says and never settles against a disabled policy or an operator's deactivation. See README.md, "Settlement by deadline".
func (m *Manager) parkAtDeadline(ctx context.Context, snapshot chain.PhaseSnapshot, devshards []store.DevshardRecord) ([]store.DevshardRecord, error) {
	if m.chainFacts == nil || snapshotHasNoEpochYet(snapshot) {
		return devshards, nil
	}
	policy := newSettlementPolicy(m.config.Load().Rotation)
	narrated := map[string]bool{}
	narrate := func(record store.DevshardRecord, deadline settleDeadline, reason deadlineUnsettled) {
		key := record.EscrowID + "|" + string(reason)
		narrated[key] = true
		if m.deadlineNarrated[key] {
			return
		}
		m.deadlineCounts.add(record.Model, string(reason))
		if m.narrator != nil {
			m.narrator.EscrowDeadlineUnsettled(record.EscrowID, deadline.settleBy, string(reason))
		}
	}
	updated := slices.Clone(devshards)
	var errs []error
	for index, record := range updated {
		deadline := m.deadlineOf(record, snapshot)
		if !deadline.inMargin || goneFromChain(record) {
			continue
		}
		if deadline.passed {
			narrate(record, deadline, deadlinePassed)
		}
		switch {
		case !policy.settles(record.Model, ownEpochOver(record, snapshot)):
			narrate(record, deadline, deadlineSettlementDisabled)
		case !record.Active && !record.SettlementPending && record.SettleTxHash == "":
			narrate(record, deadline, m.deactivationReason(record))
		case record.Active:
			parked, err := m.parkIfServing(ctx, record.EscrowID)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			if !parked {
				continue
			}
			if m.narrator != nil {
				epoch, _ := chainEpochOf(record)
				m.narrator.EscrowDeadlineReached(record.EscrowID, epoch, deadline.settleBy)
			}
			updated[index].Active, updated[index].SettlementPending = false, true
		}
	}
	m.deadlineNarrated = narrated
	return updated, errors.Join(errs...)
}

func (m *Manager) deactivationReason(record store.DevshardRecord) deadlineUnsettled {
	if _, err := m.signer.SignerFor(record.PrivateKeyEnv); err != nil {
		return deadlineKeyMissing
	}
	return deadlineOperatorDeactivated
}

// prunedChecksPerTick bounds the chain lookups markPrunedPastDeadline makes in one tick; noSettleMargin judges a deadline at the switch block itself.
const (
	prunedChecksPerTick       = 16
	noSettleMargin      int64 = 0
)

type prunedCheck int

const (
	prunedCheckKept prunedCheck = iota
	prunedCheckMarkedGone
	prunedCheckDropped
)

// markPrunedPastDeadline records what settlement will never pick up: a row out of service past its deadline whose escrow the chain has pruned. See README.md, "Settlement by deadline".
func (m *Manager) markPrunedPastDeadline(ctx context.Context, snapshot chain.PhaseSnapshot, devshards []store.DevshardRecord) ([]store.DevshardRecord, error) {
	if m.chainFacts == nil {
		return devshards, nil
	}
	policy := newSettlementPolicy(m.config.Load().Rotation)
	checked := slices.Clone(devshards)
	candidates := make([]int, 0, len(checked))
	for index, record := range checked {
		settlementOwnsIt := record.SettlementPending && policy.settles(record.Model, ownEpochOver(record, snapshot))
		if record.Active || goneFromChain(record) || settlementOwnsIt || !deadlineAt(record, snapshot, noSettleMargin).passed {
			continue
		}
		candidates = append(candidates, index)
	}
	escrowIDAt := func(index int) string { return checked[index].EscrowID }
	dropped := make(map[string]bool)
	var errs []error
	for _, index := range nextInTurn(candidates, escrowIDAt, m.prunedCheckAfter, prunedChecksPerTick) {
		m.prunedCheckAfter = escrowIDAt(index)
		outcome, err := m.checkPruned(ctx, checked[index])
		switch {
		case err != nil:
			errs = append(errs, err)
		case outcome == prunedCheckMarkedGone:
			checked[index].GoneFromChain = true
		case outcome == prunedCheckDropped:
			dropped[escrowIDAt(index)] = true
		}
	}
	return slices.DeleteFunc(checked, func(record store.DevshardRecord) bool { return dropped[record.EscrowID] }), errors.Join(errs...)
}

// checkPruned drops a row whose settlement was confirmed, as settlement would have, and records any other as a loss.
func (m *Manager) checkPruned(ctx context.Context, record store.DevshardRecord) (prunedCheck, error) {
	_, found, err := m.chainFacts.GetEscrow(ctx, record.EscrowID)
	switch {
	case err != nil:
		return prunedCheckKept, fmt.Errorf("checking escrow %s past its deadline: %w", record.EscrowID, err)
	case found:
		return prunedCheckKept, nil
	case record.SettleTxHash != "" && !record.SettlementPending:
		if err := m.deleteSettled(ctx, record.EscrowID); err != nil {
			return prunedCheckKept, err
		}
		return prunedCheckDropped, nil
	}
	if err := m.settlementSource.Retire(record.EscrowID); err != nil {
		return prunedCheckKept, fmt.Errorf("retiring escrow %s from routing: %w", record.EscrowID, err)
	}
	if err := m.store.WithRetry(ctx, func() error { return m.store.MarkDevshardGoneFromChain(ctx, record.EscrowID) }); err != nil {
		return prunedCheckKept, fmt.Errorf("marking escrow %s gone from chain: %w", record.EscrowID, err)
	}
	if m.narrator != nil {
		m.narrator.EscrowGoneFromChain(record.EscrowID)
	}
	return prunedCheckMarkedGone, nil
}
