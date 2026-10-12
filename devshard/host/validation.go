package host

import (
	"devshard"
	"devshard/observability"
	"devshard/types"
)

// ValidationStatus reports eligibility and whether this host already queued a result.
func (h *Host) ValidationStatus(inferenceID uint64) (eligible, queued bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	rec, exists := h.sm.GetInference(inferenceID)
	return exists && h.inferenceValidatable(&rec), h.hasMempoolValidationOrVote(inferenceID)
}

// PublishValidation selects the transaction using current state under the host lock.
// It returns "" for obsolete work, "present" for an existing result, or the added tx type.
// guard must check only local state (epoch/TTL), never perform storage I/O.
func (h *Host) PublishValidation(inferenceID uint64, valid bool, guard func() error) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	check := func() error {
		if !devshard.CanValidateEpoch(h.validator, h.epochID) {
			return devshard.ErrValidationEpochUnavailable
		}
		if guard != nil {
			return guard()
		}
		return nil
	}
	if err := check(); err != nil {
		return "", err
	}
	rec, exists := h.sm.GetInference(inferenceID)
	if !exists || !h.inferenceValidatable(&rec) {
		return "", nil
	}
	if h.hasMempoolValidationOrVote(inferenceID) {
		return "present", nil
	}
	var tx *types.DevshardTx
	var kind string
	if rec.Status == types.StatusChallenged {
		msg := &types.MsgValidationVote{InferenceId: inferenceID, VoterSlot: h.PrimarySlot(), VoteValid: valid, EscrowId: h.escrowID}
		sig, err := h.signProposer(msg)
		if err != nil {
			return "", err
		}
		msg.ProposerSig = sig
		tx = &types.DevshardTx{Tx: &types.DevshardTx_ValidationVote{ValidationVote: msg}}
		kind = "validation_vote"
	} else {
		msg := &types.MsgValidation{InferenceId: inferenceID, ValidatorSlot: h.PrimarySlot(), Valid: valid, EscrowId: h.escrowID}
		sig, err := h.signProposer(msg)
		if err != nil {
			return "", err
		}
		msg.ProposerSig = sig
		tx = &types.DevshardTx{Tx: &types.DevshardTx_Validation{Validation: msg}}
		kind = "validation"
	}
	if err := check(); err != nil {
		return "", err
	}
	h.mempool.Add(MempoolEntry{Tx: tx, ProposedAt: h.sm.LatestNonce()})
	observability.SetMempoolSize(h.escrowID, h.mempool.Len())
	return kind, nil
}
