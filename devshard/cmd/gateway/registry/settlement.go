package registry

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"strconv"

	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/escrow"
	"devshard/signing"
	"devshard/state"
	"devshard/types"
)

// Finalize collects host signatures, so a non-resident escrow is rehydrated with a serving session.
func (r *Registry) Finalize(ctx context.Context, escrowID string) error {
	return r.withServingSession(ctx, escrowID, func(session EscrowSession) error {
		if err := session.Finalize(ctx); err != nil {
			return err
		}
		r.observeFinalized(escrowID, session)
		return nil
	})
}

func (r *Registry) withServingSession(ctx context.Context, escrowID string, work func(EscrowSession) error) error {
	if r.chainMaxNonce.Load() == unknownChainMaxNonce {
		return fmt.Errorf("%w: escrow %s waits for the chain max_nonce before it is settled", escrow.ErrDevshardBusy, escrowID)
	}
	if session, release, held := r.HoldSettlement(escrowID); held {
		defer release()
		return work(session)
	}
	if r.servingSessions == nil {
		return fmt.Errorf("escrow %s: no serving session factory", escrowID)
	}
	session, err := r.openServingSession(ctx, escrowID)
	if err != nil {
		return fmt.Errorf("rehydrating serving session for escrow %s: %w", escrowID, err)
	}
	return errors.Join(work(session), session.Close())
}

func (r *Registry) openServingSession(ctx context.Context, escrowID string) (EscrowSession, error) {
	session, err := r.servingSessions(ctx, escrowID)
	if err != nil {
		return nil, err
	}
	session.SetChainMaxNonce(r.chainMaxNonce.Load())
	return session, nil
}

// observeFinalized hands the ledger the escrow as finalize left it: finalize pays every open record, which a reading taken at drain never saw.
func (r *Registry) observeFinalized(escrowID string, session EscrowSession) {
	if r.retiring != nil {
		r.retiring(escrowID, session)
	}
}

// BuildSettlement rehydrates a non-resident escrow read-only: the payload comes entirely from local storage.
func (r *Registry) BuildSettlement(ctx context.Context, escrowID string) (chain.SettlementInput, error) {
	if session, release, held := r.HoldSettlement(escrowID); held {
		defer release()
		return r.buildSettlement(escrowID, session)
	}
	if r.readOnlySessions == nil {
		return chain.SettlementInput{}, fmt.Errorf("escrow %s: no read-only session factory", escrowID)
	}
	session, err := r.readOnlySessions(ctx, escrowID)
	if err != nil {
		return chain.SettlementInput{}, fmt.Errorf("rehydrating read-only session for escrow %s: %w", escrowID, err)
	}
	input, buildErr := r.buildSettlement(escrowID, session)
	return input, errors.Join(buildErr, session.Close())
}

// Inspect resolves a session for reading alone; its release closes a rehydrated one and drops the hold on a resident one.
func (r *Registry) Inspect(ctx context.Context, escrowID string) (EscrowSession, func(), error) {
	if session, release, held := r.HoldSettlement(escrowID); held {
		return session, release, nil
	}
	if r.readOnlySessions == nil {
		return nil, nil, fmt.Errorf("escrow %s: no read-only session factory", escrowID)
	}
	session, err := r.readOnlySessions(ctx, escrowID)
	if err != nil {
		return nil, nil, fmt.Errorf("inspecting escrow %s: %w", escrowID, err)
	}
	return session, func() { _ = session.Close() }, nil
}

func (r *Registry) buildSettlement(escrowID string, session EscrowSession) (chain.SettlementInput, error) {
	numericID, err := strconv.ParseUint(escrowID, 10, 64)
	if err != nil {
		return chain.SettlementInput{}, fmt.Errorf("escrow id %q is not numeric: %w", escrowID, err)
	}
	nonce := session.Nonce()
	snapshot := session.SnapshotState()
	signatures, shortfall := session.SettlementSignatures(nonce)
	if shortfall != nil {
		signatures = session.SignaturesAt(nonce)
	}
	payload, err := state.BuildSettlement(escrowID, snapshot, signatures, nonce)
	if err != nil {
		return chain.SettlementInput{}, fmt.Errorf("building settlement for escrow %s: %w", escrowID, err)
	}
	hostStats := statsPerPresentSlot(payload.HostStats)
	verified := *payload
	verified.HostStats = hostStats
	if unverifiable := cmp.Or(settlementUnverifiable(verified, snapshot), shortfall); unverifiable != nil && r.narrator != nil {
		r.narrator.SettlementUnverifiable(escrowID, nonce, unverifiable)
	}
	hostStatsHash, err := state.ComputeHostStatsHash(hostStats)
	if err != nil {
		return chain.SettlementInput{}, fmt.Errorf("hashing host stats for escrow %s: %w", escrowID, err)
	}
	return chain.SettlementInput{
		EscrowID: numericID,
		StateRoot: state.ComputeStateRootFromRestHash(
			hostStatsHash, payload.RestHash, payload.Fees, types.PhaseSettlement, payload.StateRootAndProtocolVersion),
		Nonce:     payload.Nonce,
		RestHash:  payload.RestHash,
		HostStats: settlementHostStats(hostStats),
		SlotSigs:  settlementSlotSigs(payload.Signatures),
		Fees:      payload.Fees,
		Version:   payload.StateRootAndProtocolVersion,
	}, nil
}

func statsPerPresentSlot(perSlot map[uint32]*types.HostStats) map[uint32]*types.HostStats {
	present := make(map[uint32]*types.HostStats, len(perSlot))
	for slotID, hostStats := range perSlot {
		if hostStats == nil {
			continue
		}
		present[slotID] = hostStats
	}
	return present
}

func settlementHostStats(perSlot map[uint32]*types.HostStats) []chain.SettlementHostStat {
	stats := make([]chain.SettlementHostStat, 0, len(perSlot))
	for _, slotID := range sortedKeys(perSlot) {
		hostStats := perSlot[slotID]
		stats = append(stats, chain.SettlementHostStat{
			SlotID:               uint64(slotID),
			Missed:               uint64(hostStats.Missed),
			Invalid:              uint64(hostStats.Invalid),
			Cost:                 hostStats.Cost,
			RequiredValidations:  uint64(hostStats.RequiredValidations),
			CompletedValidations: uint64(hostStats.CompletedValidations),
		})
	}
	return stats
}

func settlementSlotSigs(perSlot map[uint32][]byte) []chain.SettlementSlotSig {
	sigs := make([]chain.SettlementSlotSig, 0, len(perSlot))
	for _, slotID := range sortedKeys(perSlot) {
		sigs = append(sigs, chain.SettlementSlotSig{SlotID: uint64(slotID), Signature: perSlot[slotID]})
	}
	return sigs
}

// settlementUnverifiable reports what the chain would refuse. See README.md, "Settlement and inspection".
func settlementUnverifiable(payload state.SettlementPayload, snapshot types.EscrowState) error {
	if len(snapshot.Group) == 0 {
		return nil
	}
	_, err := state.VerifySettlement(payload, snapshot.Group, signing.NewSecp256k1Verifier(), snapshot.WarmKeys)
	return err
}
