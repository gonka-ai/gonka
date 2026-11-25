package registry

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"strconv"

	"devshard/cmd/gateway/chain"
	"devshard/gatewayprotocol/settlement"
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
		return fmt.Errorf("%w: escrow %s waits for the chain max_nonce before it is settled", chain.ErrDevshardBusy, escrowID)
	}
	if session, release, held := r.HoldSettlement(escrowID); held {
		defer release()
		return work(session)
	}
	if r.closeInProgress(escrowID) {
		return fmt.Errorf("%w: escrow %s is closing its session", chain.ErrDevshardBusy, escrowID)
	}
	if r.servingSessions == nil {
		return fmt.Errorf("escrow %s: no serving session factory", escrowID)
	}
	session, err := r.openServingSession(ctx, escrowID)
	if err != nil {
		return fmt.Errorf("rehydrating serving session for escrow %s: %w", escrowID, err)
	}
	if r.rehydrated != nil {
		r.rehydrated(escrowID, session)
	}
	return errors.Join(work(session), session.Close())
}

func (r *Registry) closeInProgress(escrowID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, known := r.settlementEntryLocked(escrowID)
	return known && entry.closeClaimed
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
func (r *Registry) BuildSettlement(ctx context.Context, escrowID string) (settlement.Message, error) {
	if session, release, held := r.HoldSettlement(escrowID); held {
		defer release()
		return r.buildSettlement(escrowID, session)
	}
	if r.readOnlySessions == nil {
		return settlement.Message{}, fmt.Errorf("escrow %s: no read-only session factory", escrowID)
	}
	session, err := r.readOnlySessions(ctx, escrowID)
	if err != nil {
		return settlement.Message{}, fmt.Errorf("rehydrating read-only session for escrow %s: %w", escrowID, err)
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

func (r *Registry) buildSettlement(escrowID string, session EscrowSession) (settlement.Message, error) {
	numericID, err := strconv.ParseUint(escrowID, 10, 64)
	if err != nil {
		return settlement.Message{}, fmt.Errorf("escrow id %q is not numeric: %w", escrowID, err)
	}
	nonce := session.Nonce()
	snapshot := session.SnapshotState()
	signatures, err := settlement.ChooseSignatures(session, nonce, len(snapshot.Group))
	if err != nil {
		return settlement.Message{}, fmt.Errorf("choosing settlement signatures for escrow %s: %w", escrowID, err)
	}
	payload, err := state.BuildSettlement(escrowID, snapshot, signatures.BySlot, nonce)
	if err != nil {
		return settlement.Message{}, fmt.Errorf("building settlement for escrow %s: %w", escrowID, err)
	}
	hostStats := settlement.PresentStats(payload.HostStats)
	verified := *payload
	verified.HostStats = hostStats
	if unverifiable := cmp.Or(settlementUnverifiable(verified, snapshot), signatures.Shortfall); unverifiable != nil && r.narrator != nil {
		r.narrator.SettlementUnverifiable(escrowID, nonce, unverifiable)
	}
	root, err := settlement.Root(verified)
	if err != nil {
		return settlement.Message{}, fmt.Errorf("hashing host stats for escrow %s: %w", escrowID, err)
	}
	return settlement.Message{
		EscrowID:       numericID,
		StateRoot:      root,
		Nonce:          payload.Nonce,
		RestHash:       payload.RestHash,
		HostStats:      settlement.HostStatsBySlot(hostStats),
		SlotSignatures: settlement.SignaturesBySlot(payload.Signatures),
		Fees:           payload.Fees,
		Version:        payload.StateRootAndProtocolVersion,
	}, nil
}

// settlementUnverifiable reports what the chain would refuse. See README.md, "Settlement and inspection".
func settlementUnverifiable(payload state.SettlementPayload, snapshot types.EscrowState) error {
	if len(snapshot.Group) == 0 {
		return nil
	}
	_, err := state.VerifySettlement(payload, snapshot.Group, signing.NewSecp256k1Verifier(), snapshot.WarmKeys)
	return err
}
