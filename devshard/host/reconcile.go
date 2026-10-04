package host

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"devshard/logging"
	"devshard/observability"
	"devshard/storage"
	"devshard/types"
)

// errStopAfterDiffPage ends ReadDiffPages after the first packed page.
var errStopAfterDiffPage = errors.New("stop after diff page")

// ErrReconcileGap is returned when an incoming diff skips ahead of in-memory
// LatestNonce and the durable store cannot supply a contiguous fill range.
var ErrReconcileGap = errors.New("cannot reconcile nonce gap from store")

// applyAndPersistReconciling applies a diff after healing any nonce gap from
// durable storage (HA stale-standby path). Caller must hold h.mu on entry and
// still holds it on return; the lock may be released briefly for each page read.
//
// Happy path (diff.Nonce == memNonce+1 or stale): no store read.
// Gap path (diff.Nonce > memNonce+1): page (memNonce+1)..(diff.Nonce-1) from
// the store, apply each page in memory without AppendDiff, then applyAndPersist
// the incoming diff (Phase 1 makes an already-durable incoming nonce idempotent).
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
		if err := h.applyDurablePagesLocked(ctx, from, to, "reconcile", "reconcile_fast_forward", diff.Nonce); err != nil {
			return err
		}
		// Loop: apply the incoming diff, or return nil if a concurrent apply
		// closed the gap while a page was being read.
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
	return h.applyDurablePagesLocked(ctx, current+1, meta.LatestNonce, "catch-up", "reconcile_catch_up", 0)
}

// applyDurablePagesLocked fast-forwards memory through [from, to] one page at
// a time. Caller holds h.mu; it is dropped around each page read and held
// again on return. LatestNonce is re-read before each page, and records the
// host already applied are skipped. A hole returns ErrReconcileGap. A
// concurrent apply that moves memory through to returns nil.
func (h *Host) applyDurablePagesLocked(ctx context.Context, from, to uint64, errLabel, logEvent string, incoming uint64) error {
	if from > to {
		return nil
	}
	if h.store == nil {
		return fmt.Errorf("%w: %s escrow %s need %d..%d (no store)",
			ErrReconcileGap, errLabel, h.escrowID, from, to)
	}
	store := h.store
	escrowID := h.escrowID
	logged := false
	for from <= to {
		if err := ctx.Err(); err != nil {
			return err
		}
		current := h.sm.LatestNonce()
		if current >= to {
			return nil
		}
		if current+1 > from {
			from = current + 1
		}
		if from > to {
			return nil
		}

		h.mu.Unlock()
		page, err := loadOneDiffPage(store, escrowID, from, to)
		h.mu.Lock()
		if err != nil {
			var gap *storage.DiffGapError
			if errors.As(err, &gap) {
				return fmt.Errorf("%w: %s escrow %s need %d..%d: %s",
					ErrReconcileGap, errLabel, h.escrowID, from, to, gap.Error())
			}
			return fmt.Errorf("%s get diffs %d..%d: %w", errLabel, from, to, err)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if len(page) == 0 {
			return fmt.Errorf("%w: %s escrow %s need %d..%d have 0 record(s)",
				ErrReconcileGap, errLabel, h.escrowID, from, to)
		}

		for _, rec := range page {
			if rec.Nonce <= h.sm.LatestNonce() {
				continue
			}
			if !logged {
				logged = true
				args := []any{
					"subsystem", "host",
					"escrow_id", h.escrowID,
					"from", from,
					"to", to,
				}
				if incoming > 0 {
					args = append(args, "incoming", incoming)
				}
				logging.Info(logEvent, args...)
				observability.IncReconcileFastForward()
			}
			if err := h.applyDurableRecordLocked(rec); err != nil {
				return err
			}
		}
		from = h.sm.LatestNonce() + 1
	}
	return nil
}

// loadOneDiffPage reads the first page of [from, to]. A hole at from returns
// *storage.DiffGapError. A hole after a packed prefix returns that prefix;
// the next call, starting at the hole, reports the gap.
func loadOneDiffPage(store storage.DiffReader, escrowID string, from, to uint64) ([]types.DiffRecord, error) {
	var page []types.DiffRecord
	err := storage.ReadDiffPages(store, escrowID, from, to, func(p []types.DiffRecord) error {
		page = append([]types.DiffRecord(nil), p...)
		return errStopAfterDiffPage
	})
	if errors.Is(err, errStopAfterDiffPage) {
		return page, nil
	}
	return nil, err
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
