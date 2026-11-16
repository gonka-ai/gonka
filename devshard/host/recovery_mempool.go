package host

import (
	"fmt"
	"time"

	"devshard/signing"
	"devshard/types"
)

// RecoveryMempoolFor returns only evidence for this challenge. Unrelated
// proposals cannot consume its authorization budget. Missing evidence for
// completed work must be an explicit pending reply, never an empty success.
func (h *Host) RecoveryMempoolFor(id uint64) ([]*types.DevshardTx, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !types.RequiresMainnetHeightProof(h.sm.ProtocolVersion()) {
		return RecoveryTxsFor(h.mempool.Txs(), id), nil
	}
	actors := h.sm.HostSlotActorsWithBudget(100 * time.Millisecond)
	if _, finished := h.finishing[id]; finished {
		// Publication may already be waiting on a grant RPC. Return cold
		// evidence without starting another lookup; retain uncertain warm rows.
		actors = signing.SlotActors{SlotKeys: h.slotToAddr, AuthorizationError: func(uint32, string) error { return types.ErrHeaderPending }}
	}
	out, pending := h.recoveryMempoolLocked(id, actors)
	if pending {
		// Preserve usable evidence alongside uncertainty. The challenger may
		// have a different local phase and must decide which proof it needs.
		return out, fmt.Errorf("%w: recovery evidence authorization pending", types.ErrHeaderPending)
	}
	return out, nil
}

// Caller holds h.mu and owns this request-scoped actor view.
func (h *Host) recoveryMempoolLocked(id uint64, actors signing.SlotActors) ([]*types.DevshardTx, bool) {
	h.retryPendingFinishLocked(id, actors)
	pending := h.pendingFinishes[id] != nil || h.finishRecoveries[id] != nil
	if _, finished := h.finishing[id]; finished {
		pending = true
	}
	var out, invalid []*types.DevshardTx
	verifier := h.verifier
	if verifier == nil {
		verifier = signing.NewSecp256k1Verifier()
	}
	rec, hasRecord := h.sm.GetInference(id)
	for _, tx := range RecoveryTxsFor(h.mempool.Txs(), id) {
		if h.queuedHostTxSettled(tx) {
			invalid = append(invalid, tx)
			continue
		}
		if msg := tx.GetFinishInference(); msg != nil && (msg.EscrowId != h.escrowID || (hasRecord && msg.ExecutorSlot != rec.ExecutorSlot)) {
			invalid = append(invalid, tx)
			continue
		}
		allowed, known := h.queuedHostTxAuthorized(tx, actors, verifier)
		if allowed {
			out = append(out, tx)
		} else if known {
			invalid = append(invalid, tx)
		} else {
			pending = true
		}
	}
	h.mempool.RemoveIncluded(invalid)
	return out, pending
}
