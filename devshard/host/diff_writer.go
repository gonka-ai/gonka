package host

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/protobuf/proto"

	"devshard/observability"
	"devshard/storage"
	"devshard/types"
)

// diffWriter is the host's single applier of incoming diffs. Requests queue
// their diffs; one worker takes everything queued, validates it as one chain,
// writes it in one storage batch, and commits it to memory. Requests that
// queue while a batch is in flight share the next one, so N concurrent
// catch-up chunks cost one transaction each round instead of one per diff.
type diffWriter struct {
	mu      sync.Mutex
	queue   []*diffWaiter
	running bool
	// claims are nonce ranges a challenge request is applying. Guarded by h.mu.
	claims []*nonceClaim
}

// applyChunkNonces is the most diffs one hold of h.mu validates. It matches
// the gateway catch-up chunk.
const applyChunkNonces = 200

// testAfterApplyChunk, when set, runs between chunks of a long run while h.mu
// is not held.
var testAfterApplyChunk func()

// testBeforeClaimWait, when set, runs as a request is about to wait on another
// request's claim. h.mu is not held.
var testBeforeClaimWait func()

// testBeforeClaimDecode, when set, runs while a challenge claim decodes a wire
// that is not already known. h.mu is not held.
var testBeforeClaimDecode func()

// testBeforeClaimSig, when set, runs before each user-signature check of a
// challenge claim, after that diff is decoded and before the next one is.
// h.mu is not held.
var testBeforeClaimSig func()

// testBeforeStackedWait, when set, runs as a stacked claim is about to wait for
// the claim below it to reach the writer queue. h.mu is not held.
var testBeforeStackedWait func()

// testBeforeTakeQueue, when set, runs before the diff writer takes the queue.
// It holds neither h.mu nor the writer lock.
var testBeforeTakeQueue func()

// testStackedWaitLimit, when set, replaces the half-deadline cap on how long a
// stacked claim waits for the claim below it to reach the writer queue.
var testStackedWaitLimit func(remaining time.Duration) time.Duration

type diffWaiter struct {
	ctx   context.Context
	diffs []types.Diff
	// taken is set, under diffWriter.mu, once the worker owns the waiter. A
	// waiter canceled before that leaves the queue with nothing written.
	taken bool
	done  chan struct{}
	newly []types.Diff
	err   error
}

// applyDiffsLocked applies diffs in nonce order through the writer and
// returns the ones this call newly applied. Stale diffs are skipped. Caller
// holds h.mu; it is released while the diffs are queued and written, and held
// again on return.
//
// If ctx ends before the worker takes the diffs, nothing is written or applied
// and ctx.Err() is returned. Once taken, the batch completes even if ctx ends.
//
// The creator's signature is checked before a diff is queued, so a diff that
// another caller relays cannot hold a nonce against the creator's own copy.
// Diffs before the first bad signature are still applied, and its error is
// returned once they are.
func (h *Host) applyDiffsLocked(ctx context.Context, diffs []types.Diff) ([]types.Diff, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	latest := h.sm.LatestNonce()
	var pending []types.Diff
	for _, diff := range diffs {
		if err := h.checkDiffNonceLimitLocked(diff); err != nil {
			return nil, err
		}
		if diff.Nonce > latest {
			pending = append(pending, diff)
		}
	}
	if len(pending) == 0 {
		return nil, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	h.mu.Unlock()
	var sigErr error
	for i, diff := range pending {
		if err := h.sm.VerifyDiffUserSig(diff); err != nil {
			pending, sigErr = pending[:i], fmt.Errorf("validate diff nonce %d: %w", diff.Nonce, err)
			break
		}
	}
	if len(pending) == 0 {
		h.mu.Lock()
		return nil, sigErr
	}
	h.mu.Lock()

	newly, err := h.enqueueDiffsLocked(ctx, pending, nil)
	if err != nil {
		return newly, err
	}
	return newly, sigErr
}

// enqueueDiffsLocked queues pending and waits for the writer. Signatures are
// already checked. Caller holds h.mu; it is released while the batch runs and
// held again on return. queued, when non-nil, is closed once the batch is in
// the writer queue and before h.mu is released, so a claim stacked above this
// one can enqueue into the same writer batch.
func (h *Host) enqueueDiffsLocked(ctx context.Context, pending []types.Diff, queued chan struct{}) ([]types.Diff, error) {
	w := &diffWaiter{ctx: ctx, diffs: pending, done: make(chan struct{})}
	h.writer.mu.Lock()
	h.writer.queue = append(h.writer.queue, w)
	start := !h.writer.running
	h.writer.running = true
	h.writer.mu.Unlock()
	if queued != nil {
		close(queued)
	}

	h.mu.Unlock()
	if start {
		go h.runDiffWriter()
	}
	select {
	case <-w.done:
	case <-ctx.Done():
		h.writer.mu.Lock()
		if !w.taken {
			h.writer.queue = slices.DeleteFunc(h.writer.queue, func(q *diffWaiter) bool { return q == w })
			h.writer.mu.Unlock()
			h.mu.Lock()
			return nil, ctx.Err()
		}
		h.writer.mu.Unlock()
		<-w.done
	}
	h.mu.Lock()
	return w.newly, w.err
}

// runDiffWriter drains the queue one batch at a time and exits when it is
// empty. At most one runs per host.
func (h *Host) runDiffWriter() {
	for {
		if testBeforeTakeQueue != nil {
			testBeforeTakeQueue()
		}
		h.writer.mu.Lock()
		queued := h.writer.queue
		h.writer.queue = nil
		if len(queued) == 0 {
			h.writer.running = false
			h.writer.mu.Unlock()
			return
		}
		batch := queued[:0]
		for _, w := range queued {
			if err := w.ctx.Err(); err != nil {
				w.err = err
				close(w.done)
				continue
			}
			w.taken = true
			batch = append(batch, w)
		}
		h.writer.mu.Unlock()

		if len(batch) > 0 {
			h.writeDiffBatch(batch)
			for _, w := range batch {
				close(w.done)
			}
		}
	}
}

// writeDiffBatch applies the union of the waiters' diffs and fills in each
// waiter's reply. A waiter fails when any of its diffs is still ahead of
// memory at the end.
func (h *Host) writeDiffBatch(ws []*diffWaiter) {
	ctx, cancel := batchContext(ws)
	defer cancel()

	h.mu.Lock()
	defer h.mu.Unlock()

	latest := h.sm.LatestNonce()
	byNonce := make(map[uint64]types.Diff)
	for _, w := range ws {
		for _, diff := range w.diffs {
			if diff.Nonce <= latest {
				continue
			}
			if have, ok := byNonce[diff.Nonce]; ok {
				if !sameDiff(have, diff) {
					w.err = fmt.Errorf("%w: escrow %s nonce %d: conflicting diffs in one batch", storage.ErrDiffFork, h.escrowID, diff.Nonce)
					break
				}
				continue
			}
			byNonce[diff.Nonce] = diff
		}
	}
	nonces := make([]uint64, 0, len(byNonce))
	for n := range byNonce {
		nonces = append(nonces, n)
	}
	slices.Sort(nonces)

	applied := make(map[uint64]bool, len(nonces))
	var batchErr error
	for i := 0; i < len(nonces); {
		j := i
		for j+1 < len(nonces) && nonces[j+1] == nonces[j]+1 {
			j++
		}
		run := make([]types.Diff, 0, j-i+1)
		for _, n := range nonces[i : j+1] {
			run = append(run, byNonce[n])
		}
		if err := h.applyRunLocked(ctx, run, applied); err != nil {
			batchErr = err
			break
		}
		i = j + 1
	}

	// A committed diff is reported once, to the first waiter that succeeded
	// with it, as if the requests had applied one after another: heartbeat
	// acks and receipts key off it.
	final := h.sm.LatestNonce()
	for _, w := range ws {
		if w.err == nil && w.diffs[len(w.diffs)-1].Nonce > final {
			w.err = batchErr
			if w.err == nil {
				w.err = fmt.Errorf("%w: escrow %s at %d, request needs %d", ErrReconcileGap, h.escrowID, final, w.diffs[len(w.diffs)-1].Nonce)
			}
		}
		if w.err != nil {
			continue
		}
		for _, diff := range w.diffs {
			if applied[diff.Nonce] {
				w.newly = append(w.newly, diff)
				delete(applied, diff.Nonce)
			}
		}
	}
}

// applyRunLocked applies one contiguous run: heal any gap below it from the
// store, then validate and commit at most applyChunkNonces diffs per hold of
// h.mu. Caller holds h.mu.
func (h *Host) applyRunLocked(ctx context.Context, run []types.Diff, applied map[uint64]bool) error {
	for len(run) > 0 {
		latest := h.sm.LatestNonce()
		for len(run) > 0 && run[0].Nonce <= latest {
			run = run[1:]
		}
		if len(run) == 0 {
			return nil
		}
		if run[0].Nonce != latest+1 {
			if h.store == nil {
				return fmt.Errorf("%w: escrow %s mem=%d incoming=%d (no store)",
					ErrReconcileGap, h.escrowID, latest, run[0].Nonce)
			}
			if err := h.applyDurablePagesLocked(ctx, latest+1, run[0].Nonce-1, "reconcile", "reconcile_fast_forward", run[0].Nonce); err != nil {
				return err
			}
			continue
		}
		n := len(run)
		if n > applyChunkNonces {
			n = applyChunkNonces
		}
		if err := h.applyChunkLocked(ctx, run[:n], applied); err != nil {
			return err
		}
		run = run[n:]
		if len(run) == 0 {
			return nil
		}
		h.mu.Unlock()
		if testAfterApplyChunk != nil {
			testAfterApplyChunk()
		}
		h.mu.Lock()
	}
	return nil
}

func (h *Host) applyChunkLocked(ctx context.Context, run []types.Diff, applied map[uint64]bool) error {
	if h.store == nil {
		for _, diff := range run {
			phaseBefore := h.sm.Phase()
			if _, err := h.sm.ApplyDiff(diff); err != nil {
				return fmt.Errorf("apply diff nonce %d: %w", diff.Nonce, err)
			}
			h.afterCommitLocked(diff, phaseBefore)
			applied[diff.Nonce] = true
		}
		return nil
	}

	warm := h.sm.WarmKeys()
	vds, validateErr := h.sm.ValidateDiffs(run)
	if len(vds) > 0 {
		recs := make([]types.DiffRecord, len(vds))
		for k, vd := range vds {
			recs[k] = types.DiffRecord{Diff: run[k], StateHash: vd.Root, WarmKeyDelta: types.ComputeWarmKeyDelta(warm, vd.WarmAfter)}
			warm = vd.WarmAfter
		}
		h.mu.Unlock()
		persistErr := storage.PersistDiffBatch(ctx, h.store, h.escrowID, recs)
		h.mu.Lock()
		if persistErr != nil {
			return observability.Classify(observability.ReasonPersistDiffErr, observability.WhereHostApplyDiff,
				fmt.Errorf("persist diff nonce %d..%d: %w", recs[0].Nonce, recs[len(recs)-1].Nonce, persistErr))
		}
		// CommitValidated reports false when a store catch-up already applied
		// that nonce while h.mu was released for the write.
		for k, vd := range vds {
			phaseBefore := h.sm.Phase()
			if !h.sm.CommitValidated(vd) {
				continue
			}
			h.afterCommitLocked(run[k], phaseBefore)
			applied[run[k].Nonce] = true
		}
	}
	if validateErr != nil {
		return fmt.Errorf("validate diff nonce %d: %w", run[len(vds)].Nonce, validateErr)
	}
	return nil
}

// afterCommitLocked runs the host-side bookkeeping for a diff just applied to
// memory. Caller holds h.mu.
func (h *Host) afterCommitLocked(diff types.Diff, phaseBefore types.SessionPhase) {
	h.mempool.RemoveIncluded(diff.Txs)
	for _, tx := range diff.Txs {
		if fi := tx.GetFinishInference(); fi != nil {
			h.recordFinishObsLocked(fi.InferenceId, diff.Nonce, time.Now())
		}
	}
	if h.store == nil {
		return
	}
	// Validation obs recording runs only after the diff is committed.
	// Correctness depends on the trial apply rejecting late/sealed
	// validations before this runs; do not move recording before commit.
	h.recordValidationObsFromAppliedDiff(diff.Txs)
	phaseAfter := h.sm.Phase()
	settledNow := phaseBefore != types.PhaseSettlement && phaseAfter == types.PhaseSettlement
	shouldSnapshot := settledNow || diff.Nonce%SnapshotInterval == 0
	h.maybeSaveSnapshotLocked(diff.Nonce, shouldSnapshot, settledNow)
}

// batchContext bounds a batch by its waiters: it is canceled once every
// waiter was canceled, and its deadline is the latest waiter deadline (none if
// any waiter has none). An expired deadline is not a cancel, so a batch that
// runs out of time reports storage busy rather than a cancel.
func batchContext(ws []*diffWaiter) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	var latest time.Time
	bounded := true
	for _, w := range ws {
		d, ok := w.ctx.Deadline()
		if !ok {
			bounded = false
			break
		}
		if d.After(latest) {
			latest = d
		}
	}
	cancelDeadline := context.CancelFunc(func() {})
	if bounded {
		ctx, cancelDeadline = context.WithDeadline(ctx, latest)
	}
	var remaining atomic.Int32
	remaining.Store(int32(len(ws)))
	stops := make([]func() bool, 0, len(ws))
	for _, w := range ws {
		wctx := w.ctx
		stops = append(stops, context.AfterFunc(wctx, func() {
			if errors.Is(wctx.Err(), context.Canceled) && remaining.Add(-1) == 0 {
				cancel()
			}
		}))
	}
	return ctx, func() {
		for _, stop := range stops {
			stop()
		}
		cancelDeadline()
		cancel()
	}
}

// sameDiff compares what the creator signed. Every queued diff has a verified
// creator signature, so two copies that differ only in the signature bytes
// (a second encoding of the same signature) apply the same state.
func sameDiff(a, b types.Diff) bool {
	if a.Nonce != b.Nonce || len(a.Txs) != len(b.Txs) ||
		string(a.PostStateRoot) != string(b.PostStateRoot) {
		return false
	}
	for i := range a.Txs {
		if !proto.Equal(a.Txs[i], b.Txs[i]) {
			return false
		}
	}
	return true
}
