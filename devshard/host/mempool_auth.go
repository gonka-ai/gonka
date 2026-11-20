package host

import (
	"devshard/heightsync"
	"devshard/signing"
	"devshard/types"
	"google.golang.org/protobuf/proto"
	"time"
)

// authorizedMempoolTxs discards host-signed proposals whose slot or key no
// longer authorizes them. Otherwise a revoked proposal can never be included,
// yet block replacement validation or trip the stale-mempool signing policy.
// Caller holds h.mu. This is live admission, not historical replay validity.
func (h *Host) authorizedMempoolTxs() []*types.DevshardTx {
	txs := h.mempool.Txs()
	if !types.RequiresMainnetHeightProof(h.sm.ProtocolVersion()) || (len(txs) == 0 && len(h.pendingFinishes) == 0) {
		return txs
	}
	return h.authorizedMempoolTxsWithActors(h.sm.HostSlotActorsWithBudget(100 * time.Millisecond))
}

func (h *Host) authorizedMempoolTxsWithActors(actors signing.SlotActors) []*types.DevshardTx {
	h.retryPendingFinishesLocked(actors)
	txs := h.mempool.Txs()
	verifier := h.verifier
	if verifier == nil {
		verifier = signing.NewSecp256k1Verifier()
	}
	out := make([]*types.DevshardTx, 0, len(txs))
	var invalid []*types.DevshardTx
	for _, tx := range txs {
		if h.queuedHostTxSettled(tx) {
			invalid = append(invalid, tx)
			continue
		}
		allowed, known := h.queuedHostTxAuthorized(tx, actors, verifier)
		if allowed {
			out = append(out, tx)
		} else if known {
			invalid = append(invalid, tx)
		}
	}

	h.mempool.RemoveIncluded(invalid)
	return out
}

// Inclusion is a logical operation, not a signature byte string: overlapping
// HA keys can sign different bytes for the same receipt, finish or validation.
// Consult committed state so an invalid/skipped message in a diff cannot erase
// owed work. Missing future records remain unresolved until their start arrives.
func (h *Host) queuedHostTxSettled(tx *types.DevshardTx) bool {
	var id uint64
	var slot uint32
	validation := false
	switch {
	case tx.GetConfirmStart() != nil:
		id = tx.GetConfirmStart().InferenceId
	case tx.GetFinishInference() != nil:
		id = tx.GetFinishInference().InferenceId
	case tx.GetValidation() != nil:
		id, slot, validation = tx.GetValidation().InferenceId, tx.GetValidation().ValidatorSlot, true
	case tx.GetValidationVote() != nil:
		id, slot, validation = tx.GetValidationVote().InferenceId, tx.GetValidationVote().VoterSlot, true
	default:
		return false
	}
	rec, ok := h.sm.GetInference(id)
	if !ok {
		return id <= h.sm.LatestNonce()
	}
	if tx.GetConfirmStart() != nil {
		return rec.Status != types.StatusPending
	}
	if tx.GetFinishInference() != nil {
		return rec.Status != types.StatusPending && rec.Status != types.StatusStarted
	}
	if validation {
		if rec.Status == types.StatusTimedOut || h.sm.Phase() == types.PhaseSettlement {
			return true
		}
		for _, sibling := range h.addrToSlots[h.slotToAddr[slot]] {
			if rec.ValidatedBy.IsSet(sibling) {
				return true
			}
		}
	}
	return false
}

// queuedHostTxAuthorized reports both live usability and whether a rejection
// is conclusive. Unresolved grant/context evidence must retain the original tx.
func (h *Host) queuedHostTxAuthorized(tx *types.DevshardTx, actors signing.SlotActors, verifier signing.Verifier) (allowed, known bool) {
	var slot uint32
	var sig, data []byte
	var message proto.Message
	if ack := tx.GetHeightAck(); ack != nil {
		recovered, err := heightsync.RecoverAckSigner(verifier, ack)
		if err != nil {
			return false, true
		}
		if actors.Allows(ack.SlotId, recovered) {
			return true, true
		}
		return false, queuedKeyRejectionKnown(actors, ack.SlotId, recovered)
	}

	switch {
	case tx.GetValidation() != nil:
		msg := proto.Clone(tx.GetValidation()).(*types.MsgValidation)
		slot, sig = msg.ValidatorSlot, msg.ProposerSig
		msg.ProposerSig = nil
		message = msg
	case tx.GetValidationVote() != nil:
		msg := proto.Clone(tx.GetValidationVote()).(*types.MsgValidationVote)
		slot, sig = msg.VoterSlot, msg.ProposerSig
		msg.ProposerSig = nil
		message = msg
	case tx.GetFinishInference() != nil:
		msg := proto.Clone(tx.GetFinishInference()).(*types.MsgFinishInference)
		slot, sig = msg.ExecutorSlot, msg.ProposerSig
		msg.ProposerSig = nil
		message = msg
	case tx.GetConfirmStart() != nil:
		msg := tx.GetConfirmStart()
		rec, ok := h.sm.GetInference(msg.InferenceId)
		if !ok {
			// Before its start arrives this is unresolved; an absent record
			// at an already applied nonce can never be introduced retroactively.
			return false, msg.InferenceId <= h.sm.LatestNonce()
		}
		slot, sig = rec.ExecutorSlot, msg.ExecutorSig
		var err error
		data, err = proto.Marshal(&types.ExecutorReceiptContent{InferenceId: msg.InferenceId, PromptHash: rec.PromptHash, Model: rec.Model, InputLength: rec.InputLength, MaxTokens: rec.MaxTokens, EscrowId: h.escrowID, ObservedHeight: msg.ObservedHeight, ObservedBlockHash: msg.ObservedBlockHash})
		if err != nil {
			return false, true
		}
	default:
		return true, true // other proposal kinds have their own admission rules
	}
	if message != nil {
		var err error
		data, err = types.CanonicalSignedBytes(message)
		if err != nil {
			return false, true
		}
	}
	recovered, err := verifier.RecoverAddress(data, sig)
	if err != nil {
		return false, true
	}
	if actors.Allows(slot, recovered) {
		return true, true
	}
	return false, queuedKeyRejectionKnown(actors, slot, recovered)
}

// A nonce revoke is conclusive even if another active binding has an unavailable
// chain lookup. A later explicit rebind is handled by the active own binding.
func queuedKeyRejectionKnown(actors signing.SlotActors, slot uint32, recovered string) bool {
	if _, ok := actors.Expected(slot); !ok {
		return true
	}
	if actors.History.RevokedOn(slot, recovered, actors.Nonce) {
		own := false
		for _, addr := range actors.History.ActiveAt(slot, actors.Nonce) {
			if addr == recovered {
				own = true
				break
			}
		}
		if !own {
			return true
		}
	}
	return actors.AuthorizationError == nil || actors.AuthorizationError(slot, recovered) == nil
}
