package state

import (
	"fmt"

	"devshard/types"
)

// EvidenceSequence is the order a ConfirmStart and a Finish are checked in.
// Verifiers and the gateway both build it, then call CheckEvidence, so the
// two cannot grow a second opinion of what would apply. A finish for a record
// that is still pending is checked behind confirm; with no confirm there is
// nothing to check, because that finish cannot apply.
func EvidenceSequence(rec *types.InferenceRecord, confirm, tx *types.DevshardTx) []*types.DevshardTx {
	if rec == nil || tx == nil {
		return nil
	}
	if tx.GetFinishInference() != nil && rec.Status == types.StatusPending {
		if confirm == nil || confirm.GetConfirmStart() == nil {
			return nil
		}
		return []*types.DevshardTx{confirm, tx}
	}
	return []*types.DevshardTx{tx}
}

// CheckEvidence reports whether txs, a ConfirmStart and/or a Finish for one
// inference applied in order to rec, would all land: the checks
// applyConfirmStart and applyFinishInference run, then the log plane. Evidence
// that passes here but fails apply would reject a timeout that nothing can
// close, so the two share their checks. Verifiers call this. The gateway calls
// CheckEvidenceCached, which is the same function with the resolver off.
//
// Nothing is mutated and no warm key is bound: the history changes only
// through diffs. A key the history does not cover goes to the grant source,
// without sm.mu held.
func (sm *StateMachine) CheckEvidence(rec *types.InferenceRecord, txs ...*types.DevshardTx) error {
	return sm.checkEvidence(rec, txs, true)
}

// CheckEvidenceCached is CheckEvidence with the grant source off. The gateway
// calls this while holding the session lock. A key the history does not cover
// fails, and the call makes no bridge request.
func (sm *StateMachine) CheckEvidenceCached(rec *types.InferenceRecord, txs ...*types.DevshardTx) error {
	return sm.checkEvidence(rec, txs, false)
}

// unboundSigner is a recovered key that only the grant source can admit.
type unboundSigner struct {
	recovered, expected string
	err                 error
}

func (sm *StateMachine) checkEvidence(rec *types.InferenceRecord, txs []*types.DevshardTx, resolve bool) error {
	unbound, err := sm.checkEvidenceState(rec, txs, resolve)
	if err != nil {
		return err
	}
	for _, u := range unbound {
		if !sm.CheckWarmKey(u.recovered, u.expected) {
			return u.err
		}
	}
	return nil
}

func (sm *StateMachine) checkEvidenceState(rec *types.InferenceRecord, txs []*types.DevshardTx, resolve bool) ([]unboundSigner, error) {
	if rec == nil || len(txs) == 0 {
		return nil, fmt.Errorf("%w: no evidence", types.ErrInvalidTransition)
	}
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	expected, ok := sm.slotToAddress[rec.ExecutorSlot]
	if !ok {
		return nil, fmt.Errorf("%w: slot %d", types.ErrSlotNotInGroup, rec.ExecutorSlot)
	}
	var unbound []unboundSigner
	// The apply-path SlotActors check, with the grant source deferred: a key
	// the history covers (or revoked) is decided here, as apply decides it;
	// any other key is collected and asked of the grant source after the
	// lock is released.
	actors := sm.consensusActorsLocked()
	if resolve {
		actors.AcceptWarm = func(_ uint32, recovered, expected string) bool {
			unbound = append(unbound, unboundSigner{recovered: recovered, expected: expected})
			return true
		}
	}
	allowed := actors.Allows

	status := rec.Status
	var inferenceID uint64
	for i, tx := range txs {
		n := len(unbound)
		var id uint64
		var sigErr error
		var err error
		switch {
		case tx.GetConfirmStart() != nil:
			msg := tx.GetConfirmStart()
			id, sigErr = msg.InferenceId, types.ErrInvalidExecutorSig
			if status != types.StatusPending {
				return nil, fmt.Errorf("%w: confirm expects pending, got %d", types.ErrInvalidTransition, status)
			}
			err = sm.checkReceiptLocked(rec, msg, allowed)
			status = types.StatusStarted
		case tx.GetFinishInference() != nil:
			msg := tx.GetFinishInference()
			id, sigErr = msg.InferenceId, types.ErrInvalidProposerSig
			if status != types.StatusStarted {
				return nil, fmt.Errorf("%w: finish expects started, got %d", types.ErrInvalidTransition, status)
			}
			_, err = sm.checkFinishLocked(rec, msg, allowed)
			status = types.StatusFinished
		default:
			return nil, fmt.Errorf("%w: evidence is a ConfirmStart or a Finish", types.ErrInvalidTransition)
		}
		if err != nil {
			return nil, err
		}
		if i == 0 {
			inferenceID = id
		} else if id != inferenceID {
			return nil, fmt.Errorf("%w: evidence for inference %d and %d", types.ErrInvalidTransition, inferenceID, id)
		}
		for j := n; j < len(unbound); j++ {
			unbound[j].err = fmt.Errorf("%w: expected %s (slot %d), got %s", sigErr, expected, rec.ExecutorSlot, unbound[j].recovered)
		}
	}
	// Compose drops a tx the log plane rejects, such as a stamp below the
	// height floor at its inference id. That floor is fixed by the diffs
	// before the start, so every replica computes the same verdict.
	if _, err := sm.logPlaneErrLocked(sm.state.LatestNonce+1, txs); err != nil {
		return nil, err
	}
	return unbound, nil
}
