package host

import (
	"context"
	"errors"

	"devshard"
	"devshard/types"
)

// retryPendingFinishesLocked never reruns the model. A completed result is
// retained unsigned during a grant-source outage, then signed only when the
// current slot authority is known again. Caller holds h.mu.
func (h *Host) retryPendingFinishesLocked() {
	for id, msg := range h.pendingFinishes {
		tx := &types.DevshardTx{Tx: &types.DevshardTx_FinishInference{FinishInference: msg}}
		if h.queuedHostTxSettled(tx) {
			delete(h.pendingFinishes, id)
			continue
		}
		sig, err := h.signProposer(msg)
		if err != nil {
			continue
		}
		msg.ProposerSig = sig
		h.mempool.Add(MempoolEntry{Tx: tx, ProposedAt: h.sm.LatestNonce()})
		delete(h.pendingFinishes, id)
	}
}

// recoverStartedFinish handles the restart case, where the unsigned in-memory
// result was lost. An execution-timeout challenge has no prompt: recovery is
// strictly from the already persisted payload and must complete before the
// reply's mempool is read. RecoveryStoredOnly can never start a new ML run.
func (h *Host) recoverStartedFinish(ctx context.Context, id uint64) error {
	if !types.RequiresMainnetHeightProof(h.sm.ProtocolVersion()) {
		return nil
	}
	h.mu.Lock()
	rec, ok := h.sm.GetInference(id)
	_, executing := h.executing[id]
	_, pending := h.pendingFinishes[id]
	if !ok || rec.Status != types.StatusStarted || h.storedResponse == nil || executing || pending || h.mempool.HasFinish(id) || !h.CanSignSlot(rec.ExecutorSlot) {
		h.mu.Unlock()
		return nil
	}
	h.executing[id] = struct{}{}
	job := &devshard.ExecuteRequest{InferenceID: id, Model: rec.Model, PromptHash: rec.PromptHash, InputLength: rec.InputLength, MaxTokens: rec.MaxTokens, EscrowID: h.escrowID, EpochID: h.epochID, Recovery: devshard.RecoveryStoredOnly}
	h.mu.Unlock()
	_, err := h.RunExecution(ctx, job)
	if errors.Is(err, devshard.ErrNoStoredResponse) {
		return nil
	}
	return err
}
