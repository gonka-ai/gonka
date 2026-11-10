package host

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"devshard/logging"
	"devshard/observability"
	"devshard/types"
)

// ErrReconcileGap is returned when an incoming diff skips ahead of in-memory
// LatestNonce and the durable store cannot supply a contiguous fill range.
var ErrReconcileGap = errors.New("cannot reconcile nonce gap from store")

// applyAndPersistReconciling applies a diff after healing any nonce gap from
// durable storage (HA stale-standby path). Caller must hold h.mu on entry and
// still holds it on return; the lock may be released briefly for GetDiffs.
//
// Happy path (diff.Nonce == memNonce+1 or stale): no store read.
// Gap path (diff.Nonce > memNonce+1): load (memNonce+1)..(diff.Nonce-1) from
// store, apply in memory without AppendDiff, then applyAndPersist the incoming
// diff (Phase 1 makes an already-durable incoming nonce idempotent).
func (h *Host) applyAndPersistReconciling(ctx context.Context, diff types.Diff) error {
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		currentNonce := h.sm.LatestNonce()
		if diff.Nonce <= currentNonce {
			return nil
		}
		if diff.Nonce == currentNonce+1 {
			return h.applyAndPersist(ctx, diff)
		}

		// Nonce gap: memory is behind shared durable state.
		if h.store == nil {
			return fmt.Errorf("%w: escrow %s mem=%d incoming=%d (no store)",
				ErrReconcileGap, h.escrowID, currentNonce, diff.Nonce)
		}

		from := currentNonce + 1
		to := diff.Nonce - 1
		escrowID := h.escrowID
		store := h.store

		h.mu.Unlock()
		records, err := store.GetDiffs(escrowID, from, to)
		h.mu.Lock()
		if err != nil {
			return fmt.Errorf("reconcile get diffs %d..%d: %w", from, to, err)
		}

		// Another request may have advanced memory while unlocked.
		currentNonce = h.sm.LatestNonce()
		if diff.Nonce <= currentNonce {
			return nil
		}
		if diff.Nonce == currentNonce+1 {
			return h.applyAndPersist(ctx, diff)
		}

		from = currentNonce + 1
		to = diff.Nonce - 1
		records = filterDiffRecordsFrom(records, from)
		if !durableRangeComplete(records, from, to) {
			return fmt.Errorf("%w: escrow %s need %d..%d have %d record(s)",
				ErrReconcileGap, h.escrowID, from, to, len(records))
		}

		logging.Info("reconcile_fast_forward",
			"subsystem", "host",
			"escrow_id", h.escrowID,
			"from", from,
			"to", to,
			"incoming", diff.Nonce,
		)
		observability.IncReconcileFastForward()

		for _, rec := range records {
			if err := h.applyDurableRecordLocked(rec); err != nil {
				return err
			}
		}
		// Loop: apply incoming (or heal again if still gapped — shouldn't happen).
	}
}

// CatchUpFromStore fast-forwards in-memory state to the durable tip. An HA
// replica that bound the session at an earlier nonce otherwise stays stale
// until the next incoming diff; GET /mempool catch-up plus EnqueueDueValidations
// lets the survivor re-acquire work the owner Released on graceful stop.
func (h *Host) CatchUpFromStore(ctx context.Context) error {
	if h == nil || h.store == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	meta, err := h.store.GetSessionMeta(h.escrowID)
	if err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	current := h.sm.LatestNonce()
	if meta.LatestNonce <= current {
		return nil
	}
	from := current + 1
	to := meta.LatestNonce
	escrowID := h.escrowID
	store := h.store

	h.mu.Unlock()
	records, err := store.GetDiffs(escrowID, from, to)
	h.mu.Lock()
	if err != nil {
		return fmt.Errorf("catch-up get diffs %d..%d: %w", from, to, err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	current = h.sm.LatestNonce()
	if meta.LatestNonce <= current {
		return nil
	}
	from = current + 1
	to = meta.LatestNonce
	records = filterDiffRecordsFrom(records, from)
	if !durableRangeComplete(records, from, to) {
		return fmt.Errorf("%w: catch-up escrow %s need %d..%d have %d record(s)",
			ErrReconcileGap, h.escrowID, from, to, len(records))
	}

	logging.Info("reconcile_catch_up",
		"subsystem", "host",
		"escrow_id", h.escrowID,
		"from", from,
		"to", to,
	)
	observability.IncReconcileFastForward()
	for _, rec := range records {
		if err := h.applyDurableRecordLocked(rec); err != nil {
			return err
		}
	}
	return nil
}

// applyDurableRecordLocked applies a diff that is already durable in the store.
// It must not AppendDiff. Caller must hold h.mu.
func (h *Host) applyDurableRecordLocked(rec types.DiffRecord) error {
	currentNonce := h.sm.LatestNonce()
	if rec.Nonce <= currentNonce {
		return nil
	}
	if rec.Nonce != currentNonce+1 {
		return fmt.Errorf("%w: durable apply expected nonce %d, got %d",
			types.ErrInvalidNonce, currentNonce+1, rec.Nonce)
	}
	if err := h.checkDiffNonceLimitLocked(rec.Diff); err != nil {
		return err
	}

	phaseBefore := h.sm.Phase()
	h.sm.InjectWarmKeys(rec.WarmKeyDelta)
	root, err := h.sm.ApplyDiff(rec.Diff)
	if err != nil {
		return fmt.Errorf("reconcile apply durable nonce %d: %w", rec.Nonce, err)
	}
	if len(rec.StateHash) > 0 && len(root) > 0 && !bytes.Equal(root, rec.StateHash) {
		return fmt.Errorf("reconcile state hash mismatch at nonce %d", rec.Nonce)
	}

	h.mempool.RemoveIncluded(rec.Txs)
	for _, tx := range rec.Txs {
		if fi := tx.GetFinishInference(); fi != nil {
			delete(h.completedResponses, fi.InferenceId)
		}
		if ti := tx.GetTimeoutInference(); ti != nil {
			delete(h.completedResponses, ti.InferenceId)
		}
		if em := tx.GetErrorMiss(); em != nil {
			delete(h.completedResponses, em.InferenceId)
		}
	}
	h.recordValidationObsFromAppliedDiff(rec.Txs)
	phaseAfter := h.sm.Phase()
	settledNow := phaseBefore != types.PhaseSettlement && phaseAfter == types.PhaseSettlement
	shouldSnapshot := settledNow || rec.Nonce%SnapshotInterval == 0
	h.maybeSaveSnapshotLocked(rec.Nonce, shouldSnapshot, settledNow)
	return nil
}

func filterDiffRecordsFrom(records []types.DiffRecord, from uint64) []types.DiffRecord {
	if len(records) == 0 {
		return records
	}
	out := records[:0]
	for _, rec := range records {
		if rec.Nonce >= from {
			out = append(out, rec)
		}
	}
	return out
}

func durableRangeComplete(records []types.DiffRecord, from, to uint64) bool {
	if from > to {
		return true
	}
	want := int(to - from + 1)
	if len(records) != want {
		return false
	}
	for i, rec := range records {
		if rec.Nonce != from+uint64(i) {
			return false
		}
	}
	return true
}
