package host

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"

	"devshard/state"
	"devshard/storage"
	"devshard/types"
)

// ErrChallengeStateChanged means the shortened challenge cannot connect locally.
var ErrChallengeStateChanged = errors.New("executor state changed before challenge")

func (h *Host) checkRefusalBounds(p *types.RefusalPackage) error {
	if err := p.CheckBounds(); err != nil {
		return err
	}
	binding := h.sm.SnapshotStateNoInferences()
	if p.EscrowID != h.escrowID || p.Version != binding.StateRootAndProtocolVersion {
		return fmt.Errorf("refusal binding mismatch")
	}
	if max := h.chainMaxNonce(); max > 0 && p.T > uint64(max) {
		return types.ErrNonceLimitExceeded
	}
	for _, d := range p.Diffs {
		if err := h.checkDiffNonceLimitLocked(d); err != nil {
			return err
		}
	}
	return nil
}

func checkRefusalTarget(st *types.EscrowState, id uint64, payload *InferencePayload, expected *types.InferenceRecord) error {
	rec, ok := st.Inferences[id]
	if !ok || rec.Status != types.StatusPending || payload == nil {
		return fmt.Errorf("refusal target is not pending")
	}
	if expected != nil && (rec.ExecutorSlot != expected.ExecutorSlot || !bytes.Equal(rec.PromptHash, expected.PromptHash) || rec.Model != expected.Model || rec.InputLength != expected.InputLength || rec.MaxTokens != expected.MaxTokens || rec.StartedAt != expected.StartedAt) {
		return fmt.Errorf("refusal target mismatch")
	}
	return VerifyPayload(payload, rec.PromptHash, rec.Model, rec.InputLength, rec.MaxTokens, rec.StartedAt)
}

func (h *Host) VerifyRefusalPackage(p *types.RefusalPackage, id uint64, payload *InferencePayload) error {
	h.mu.Lock()
	err := h.checkRefusalBounds(p)
	rec, ok := h.sm.GetInference(id)
	h.mu.Unlock()
	if err != nil {
		return err
	}
	if !ok {
		return types.ErrInferenceNotFound
	}
	st, err := h.sm.VerifyRefusalPackage(p)
	if err != nil {
		return err
	}
	if err := checkRefusalTarget(st, id, payload, &rec); err != nil {
		return err
	}
	return h.checkRefusalState(p, st)
}

func (h *Host) checkRefusalState(p *types.RefusalPackage, final *types.EscrowState) error {
	h.mu.Lock()
	nonce := h.sm.LatestNonce()
	var knownRoot []byte
	var err error
	if nonce >= p.N && nonce <= p.T {
		knownRoot, err = h.sm.ComputeStateRoot()
	}
	h.mu.Unlock()
	if err != nil || nonce < p.N {
		return err
	}
	if nonce > p.T {
		if h.store == nil {
			return nil
		}
		nonce = p.T
		rows, err := h.store.GetDiffs(h.escrowID, nonce, nonce)
		if err != nil {
			return err
		}
		// Imports and pruning can leave no local root at this nonce.
		if len(rows) == 0 || len(rows[0].StateHash) == 0 {
			return nil
		}
		knownRoot = rows[0].StateHash
	}

	var suppliedRoot []byte
	if nonce > p.N {
		// Replay already checked every signed post-state root.
		suppliedRoot = p.Diffs[nonce-p.N-1].PostStateRoot
	} else {
		snapshot := final
		if p.N < p.T {
			snapshot, _, _, err = types.UnmarshalStateSnapshotProto(p.Snapshot)
			if err != nil {
				return err
			}
		}
		suppliedRoot, err = state.SnapshotRoot(snapshot)
		if err != nil {
			return err
		}
	}
	if !bytes.Equal(knownRoot, suppliedRoot) {
		return fmt.Errorf("%w: refusal state at nonce %d", types.ErrStateHashMismatch, nonce)
	}
	return nil
}

// CatchUpForChallenge leaves snapshot bytes undecoded when the tail connects.
func (h *Host) CatchUpForChallenge(ctx context.Context, p *types.RefusalPackage, id uint64, payload *InferencePayload) error {
	h.mu.Lock()
	if err := h.checkRefusalBounds(p); err != nil {
		h.mu.Unlock()
		return err
	}
	if len(p.Snapshot) == 0 {
		nonce := h.sm.LatestNonce()
		if nonce < p.N {
			h.mu.Unlock()
			return ErrChallengeStateChanged
		}
		if nonce <= p.T {
			expected := p.BaseRoot
			if nonce > p.N {
				expected = p.Diffs[nonce-p.N-1].PostStateRoot
			}
			root, err := h.sm.ComputeStateRoot()
			if err != nil || !bytes.Equal(root, expected) {
				h.mu.Unlock()
				return ErrChallengeStateChanged
			}
		}
	}
	if h.sm.LatestNonce() >= p.N {
		defer h.mu.Unlock()
		for _, diff := range p.Diffs {
			if err := h.applyAndPersistReconciling(ctx, diff); err != nil {
				return err
			}
		}
		return nil
	}
	h.mu.Unlock()
	st, err := h.sm.VerifyRefusalPackage(p)
	if err != nil {
		return err
	}
	if err = checkRefusalTarget(st, id, payload, nil); err != nil {
		return err
	}
	return h.ImportVerifiedSnapshot(st)
}

// ImportVerifiedSnapshot accepts only state returned by package verification.
func (h *Host) ImportVerifiedSnapshot(st *types.EscrowState) error {
	data, err := MarshalStateSnapshotWithCommitted(st, nil, nil)
	if err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.store == nil {
		return fmt.Errorf("snapshot import requires storage")
	}
	if h.sm.LatestNonce() < st.LatestNonce {
		err = h.store.ImportSnapshot(h.escrowID, st.LatestNonce, data)
		if err == nil {
			h.installImportedStateLocked(st)
			return nil
		}
		if !errors.Is(err, storage.ErrSnapshotAdvanced) {
			return err
		}
		if err = h.reconcileSnapshotLocked(); err != nil {
			return err
		}
	}
	if h.sm.LatestNonce() < st.LatestNonce {
		return fmt.Errorf("durable snapshot reconciliation made no progress")
	}
	if h.sm.LatestNonce() == st.LatestNonce {
		root, err := state.SnapshotRoot(st)
		if err != nil {
			return err
		}
		local, err := h.sm.ComputeStateRoot()
		if err != nil {
			return err
		}
		if !bytes.Equal(local, root) {
			return types.ErrStateHashMismatch
		}
	}
	return nil
}

// Caller holds h.mu. Storage may be ahead after another HA instance imports.
func (h *Host) reconcileSnapshotLocked() error {
	if h.store == nil {
		return fmt.Errorf("snapshot reconciliation requires storage")
	}
	meta, err := h.store.GetSessionMeta(h.escrowID)
	if err != nil {
		return err
	}
	if meta.LatestNonce <= h.sm.LatestNonce() {
		return nil
	}
	n, data, err := h.store.LoadSnapshot(h.escrowID)
	if err != nil && !errors.Is(err, storage.ErrSnapshotNotFound) {
		return err
	}
	if err == nil && n > h.sm.LatestNonce() {
		_, root, e := types.SnapshotData(data)
		if e != nil {
			return e
		}
		if meta.ImportedNonce > 0 && (n < meta.ImportedNonce || len(root) != 32) {
			return fmt.Errorf("imported snapshot missing root")
		}
		st, _, _, e := UnmarshalStateSnapshotWithCommitted(data)
		if e != nil {
			return e
		}
		binding := h.sm.SnapshotStateNoInferences()
		if st.LatestNonce != n || st.EscrowID != h.escrowID || st.StateRootAndProtocolVersion != binding.StateRootAndProtocolVersion || st.Config != binding.Config || !reflect.DeepEqual(st.Group, binding.Group) {
			return fmt.Errorf("snapshot binding mismatch")
		}
		// Legacy snapshots still need their journal root checked.
		if len(root) == 0 {
			rows, e := h.store.GetDiffs(h.escrowID, n, n)
			if e != nil {
				return e
			}
			if len(rows) != 1 || state.CheckSnapshotRoot(st, rows[0].StateHash) != nil {
				return fmt.Errorf("snapshot journal root mismatch")
			}
		}
		h.installImportedStateLocked(st)
	}
	if meta.ImportedNonce > h.sm.LatestNonce() {
		return fmt.Errorf("imported snapshot unavailable")
	}
	from := h.sm.LatestNonce() + 1
	if from <= meta.LatestNonce {
		if from == 1 || meta.LatestNonce-from >= types.RefusalTailLimit {
			return fmt.Errorf("snapshot tail unavailable")
		}
		rows, err := h.store.GetDiffs(h.escrowID, from, meta.LatestNonce)
		if err != nil {
			return err
		}
		if !durableRangeComplete(rows, from, meta.LatestNonce) {
			return ErrReconcileGap
		}
		for _, row := range rows {
			if err = h.applyDurableRecordLocked(row); err != nil {
				return err
			}
		}
	}
	return nil
}

func (h *Host) installImportedStateLocked(st *types.EscrowState) {
	h.sm.RestoreState(st)
	h.sm.RestoreSealedNonces(nil)
	var remove []*types.DevshardTx
	for _, tx := range h.mempool.Txs() {
		var id uint64
		var keep bool
		switch {
		case tx.GetConfirmStart() != nil:
			id = tx.GetConfirmStart().InferenceId
			r := st.Inferences[id]
			keep = r != nil && r.Status == types.StatusPending
		case tx.GetFinishInference() != nil:
			id = tx.GetFinishInference().InferenceId
			r := st.Inferences[id]
			// Execution can finish before confirmation is committed.
			keep = r != nil && (r.Status == types.StatusPending || r.Status == types.StatusStarted)
		case tx.GetTimeoutInference() != nil:
			msg := tx.GetTimeoutInference()
			id = msg.InferenceId
			r := st.Inferences[id]
			keep = r != nil && ((msg.Reason == types.TimeoutReason_TIMEOUT_REASON_REFUSED && r.Status == types.StatusPending) || (msg.Reason == types.TimeoutReason_TIMEOUT_REASON_EXECUTION && r.Status == types.StatusStarted))
		case tx.GetValidation() != nil:
			msg := tx.GetValidation()
			id = msg.InferenceId
			r := st.Inferences[id]
			keep = r != nil && (r.Status == types.StatusFinished || r.Status == types.StatusChallenged) && !r.ValidatedBy.IsSet(msg.ValidatorSlot)
		case tx.GetValidationVote() != nil:
			msg := tx.GetValidationVote()
			id = msg.InferenceId
			r := st.Inferences[id]
			keep = r != nil && r.Status == types.StatusChallenged && !r.ValidatedBy.IsSet(msg.VoterSlot)
		default:
			keep = true
		}
		if !keep {
			remove = append(remove, tx)
		}
	}
	h.mempool.RemoveIncluded(remove)
	for id := range h.completedResponses {
		r := st.Inferences[id]
		if r == nil || (r.Status != types.StatusPending && r.Status != types.StatusStarted) {
			delete(h.completedResponses, id)
		}
	}
}

// TimeoutState copies only the record needed by refusal/execution checks.
func (h *Host) TimeoutState(id uint64) (types.EscrowState, []*types.DevshardTx) {
	h.mu.Lock()
	defer h.mu.Unlock()
	st := h.sm.SnapshotStateNoInferences()
	st.Inferences = map[uint64]*types.InferenceRecord{}
	if rec, ok := h.sm.GetInference(id); ok {
		st.Inferences[id] = &rec
	}
	return st, h.mempool.Txs()
}
