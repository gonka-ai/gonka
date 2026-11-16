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
	out, pending := h.recoveryMempoolLocked(id, h.sm.HostSlotActorsWithBudget(100*time.Millisecond))
	usable := len(out) > 0
	if rec, ok := h.sm.GetInference(id); ok && (rec.Status == types.StatusPending || rec.Status == types.StatusStarted) {
		usable = false
		for _, tx := range out {
			if (rec.Status == types.StatusPending && tx.GetConfirmStart() != nil) || (rec.Status == types.StatusStarted && tx.GetFinishInference() != nil) {
				usable = true
			}
		}
	}
	if !usable && pending {
		return nil, fmt.Errorf("%w: recovery evidence authorization pending", types.ErrHeaderPending)
	}
	return out, nil
}

// Caller holds h.mu and owns this request-scoped actor view.
func (h *Host) recoveryMempoolLocked(id uint64, actors signing.SlotActors) ([]*types.DevshardTx, bool) {
	h.retryPendingFinishLocked(id, actors)
	pending := h.pendingFinishes[id] != nil || h.finishRecoveries[id] != nil
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
