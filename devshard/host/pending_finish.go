package host

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"devshard"
	"devshard/signing"
	"devshard/types"
)

// retryPendingFinishesLocked never reruns the model. All messages share one
// bounded, memoized actor view, rather than repeating a chain RPC per result.
// A pass signs at most 32 messages; unresolved work stays queued. Caller holds mu.
func (h *Host) retryPendingFinishesLocked(actors signing.SlotActors) {
	ids := make([]uint64, 0, len(h.pendingFinishes))
	for id := range h.pendingFinishes {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	if len(ids) == 0 {
		return
	}
	start, found := slices.BinarySearch(ids, h.pendingFinishCursor)
	if found {
		start++
	}
	// Advance the starting entry even when a pass visits the entire queue.
	// Otherwise a slow first identity can exhaust every pass's budget forever.
	h.pendingFinishCursor = ids[start%len(ids)]
	for step := 0; step < min(32, len(ids)); step++ {
		id := ids[(start+step)%len(ids)]
		h.retryPendingFinishLocked(id, actors)
	}
}

func (h *Host) retryPendingFinishLocked(id uint64, actors signing.SlotActors) {
	if h.pendingFinishes[id] == nil {
		return
	}
	msg := h.pendingFinishes[id]
	tx := &types.DevshardTx{Tx: &types.DevshardTx_FinishInference{FinishInference: msg}}
	if h.queuedHostTxSettled(tx) {
		delete(h.pendingFinishes, id)
		return
	}
	if !h.signingCandidates[msg.ExecutorSlot] || !actors.Allows(msg.ExecutorSlot, h.signer.Address()) {
		return
	}
	data, err := types.CanonicalSignedBytes(msg)
	if err != nil {
		return
	}
	sig, err := h.signer.Sign(data)
	if err != nil {
		return
	}
	msg.ProposerSig = sig
	h.mempool.Add(MempoolEntry{Tx: tx, ProposedAt: h.sm.LatestNonce()})
	delete(h.pendingFinishes, id)
}

type finishRecovery struct {
	done   chan struct{}
	cancel context.CancelFunc
	err    error // immutable after done closes
}

// recoverStartedFinish handles process loss of the pending result. Concurrent
// timeout challenges await the same storage-only recovery, and caller cancellation
// does not cancel work owed to other verifiers. No model run is permitted.
func (h *Host) recoverStartedFinish(ctx context.Context, id uint64) error {
	if !types.RequiresMainnetHeightProof(h.sm.ProtocolVersion()) {
		return nil
	}
	h.mu.Lock()
	if recovery := h.finishRecoveries[id]; recovery != nil {
		h.mu.Unlock()
		return waitFinishRecovery(ctx, recovery)
	}
	rec, ok := h.sm.GetInference(id)
	if !ok || rec.Status != types.StatusStarted || h.finishRecoveryClosed {
		h.mu.Unlock()
		return nil
	}
	actors := h.sm.HostSlotActorsWithBudget(100 * time.Millisecond)
	live, pending := h.recoveryMempoolLocked(id, actors)
	for _, tx := range live {
		if finish := tx.GetFinishInference(); finish != nil && finish.InferenceId == id {
			h.mu.Unlock()
			return nil
		}
	}
	// The remaining raw Finish or completed unsigned result has unavailable
	// authority evidence. It is owed work, not an empty successful challenge.
	if pending {
		h.mu.Unlock()
		return fmt.Errorf("%w: completed finish authorization pending", types.ErrHeaderPending)
	}
	_, executing := h.executing[id]
	if h.storedResponse == nil || executing || !h.signingCandidates[rec.ExecutorSlot] {
		h.mu.Unlock()
		return nil
	}
	if !actors.Allows(rec.ExecutorSlot, h.signer.Address()) {
		var err error
		if actors.AuthorizationError != nil {
			err = actors.AuthorizationError(rec.ExecutorSlot, h.signer.Address())
		}
		h.mu.Unlock()
		if err != nil {
			return fmt.Errorf("%w: stored finish authorization: %v", types.ErrHeaderPending, err)
		}
		return nil
	}
	recoveryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	recovery := &finishRecovery{done: make(chan struct{}), cancel: cancel}
	if h.finishRecoveries == nil {
		h.finishRecoveries = make(map[uint64]*finishRecovery)
	}
	h.finishRecoveries[id] = recovery
	h.executing[id] = struct{}{}
	h.finishRecoveryWorkers.Add(1)
	job := &devshard.ExecuteRequest{InferenceID: id, Model: rec.Model, PromptHash: rec.PromptHash, InputLength: rec.InputLength, MaxTokens: rec.MaxTokens, EscrowID: h.escrowID, EpochID: h.epochID, Recovery: devshard.RecoveryStoredOnly}
	h.mu.Unlock()
	go func() {
		defer h.finishRecoveryWorkers.Done()
		defer cancel()
		_, err := h.RunExecution(recoveryCtx, job)
		if errors.Is(err, devshard.ErrNoStoredResponse) {
			err = nil
		} else if err != nil {
			err = fmt.Errorf("%w: stored finish recovery: %v", types.ErrHeaderPending, err)
		}
		h.mu.Lock()
		recovery.err = err
		delete(h.finishRecoveries, id)
		close(recovery.done)
		h.mu.Unlock()
	}()
	return waitFinishRecovery(ctx, recovery)
}

func waitFinishRecovery(ctx context.Context, recovery *finishRecovery) error {
	// Reply pending before ordinary peer RPC deadlines, rather than allowing
	// a slow storage read to appear as an unreachable executor to the verifier.
	timer := time.NewTimer(25 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-recovery.done:
		return recovery.err
	case <-ctx.Done():
		return fmt.Errorf("%w: waiting for stored finish: %v", types.ErrHeaderPending, ctx.Err())
	case <-timer.C:
		return fmt.Errorf("%w: stored finish recovery in progress", types.ErrHeaderPending)
	}
}
