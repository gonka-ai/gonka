package host

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"google.golang.org/protobuf/proto"

	"devshard"
	"devshard/observability"
	"devshard/types"
)

// challengeClaimAttempts is how many times one challenge request waits on
// another request's claim before it returns. A wait on a claim that failed
// and left the list does not count, so failing signatures cannot burn the cap.
const challengeClaimAttempts = 8

// nonceClaim is a contiguous nonce range one challenge request is applying.
type nonceClaim struct {
	from, to uint64
	done     chan struct{}
	// queued is closed once this claim's batch is in the writer queue, before
	// the writer has applied it. Closed at most once. A claim that ends without
	// enqueuing leaves it open; waiters also select on done.
	queued chan struct{}
	err    error
	// ended is set under h.mu. done is closed once and never rewritten, so a
	// waiter blocked on it does not race with the claimant.
	ended bool
}

// ChallengeReceiptWire is ChallengeReceipt for encoded diffs. Only the claimed
// run is decoded. predecoded, when non-nil, supplies that run without a second
// decode; a nil entry falls back to DecodeDiffWire.
func (h *Host) ChallengeReceiptWire(ctx context.Context, inferenceID uint64, payload *InferencePayload, wires []types.DiffWire, predecoded []types.Diff) ([]byte, int64, error) {
	receipt, confirmedAt, job, err := h.challengeReceiptLocked(ctx, inferenceID, payload, wires, predecoded)
	if err != nil || job == nil {
		return receipt, confirmedAt, err
	}
	h.executeAsync(ctx, job)
	return receipt, confirmedAt, nil
}

func (h *Host) challengeReceiptLocked(ctx context.Context, inferenceID uint64, payload *InferencePayload, wires []types.DiffWire, predecoded []types.Diff) ([]byte, int64, *devshard.ExecuteRequest, error) {
	hdr, hdrErr := h.latestHeader(ctx)
	h.mu.Lock()
	defer h.mu.Unlock()

	_, heldBefore := h.sm.GetInference(inferenceID)
	if _, err := h.applyClaimedDiffsLocked(ctx, wires, predecoded); err != nil {
		return nil, 0, nil, fmt.Errorf("apply challenge diffs: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, 0, nil, err
	}

	rec, ok := h.sm.GetInference(inferenceID)
	if !ok || rec.Status != types.StatusPending {
		return nil, 0, nil, nil
	}
	if !h.slotIDs[rec.ExecutorSlot] {
		return nil, 0, nil, nil
	}
	if payload == nil {
		return nil, 0, nil, nil
	}
	if err := VerifyPayload(payload, rec.PromptHash, rec.Model, rec.InputLength, rec.MaxTokens); err != nil {
		return nil, 0, nil, nil
	}

	// Publish MsgConfirmStart the way the request path does. To the verifier the
	// receipt is only a liveness proof and is discarded after the timeout vote,
	// so without ConfirmStart the inference stays pending and applyFinishInference
	// rejects the MsgFinishInference this execution produces as an invalid
	// transition. Only reachable while the record is still pending.
	confirm, err := h.confirmStartLocked(inferenceID, rec, hdr, hdrErr)
	if err != nil {
		return nil, 0, nil, err
	}
	sig, confirmedAt := confirm.ExecutorSig, time.Now().Unix()

	if _, dup := h.executing[inferenceID]; dup {
		return sig, confirmedAt, nil, nil
	}
	if h.mempool.HasFinish(inferenceID) {
		return sig, confirmedAt, nil, nil
	}

	h.executing[inferenceID] = struct{}{}

	job := h.newExecuteRequest(inferenceID, rec, payload)
	if heldBefore && h.storedResponse != nil {
		job.Recovery = devshard.RecoveryStoredFirst
	}
	return sig, confirmedAt, job, nil
}

// applyClaimedDiffsLocked applies a challenge page. Nonces at or below latest
// are not decoded. Nonces an active claim already covers are waited on, not
// decoded. The rest is claimed as one contiguous run. Caller holds h.mu.
func (h *Host) applyClaimedDiffsLocked(ctx context.Context, wires []types.DiffWire, predecoded []types.Diff) ([]types.Diff, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	ordered := append([]types.DiffWire(nil), wires...)
	slices.SortFunc(ordered, func(a, b types.DiffWire) int {
		switch {
		case a.Nonce < b.Nonce:
			return -1
		case a.Nonce > b.Nonce:
			return 1
		default:
			return 0
		}
	})
	known := make(map[uint64]types.Diff, len(predecoded))
	for _, d := range predecoded {
		if _, ok := known[d.Nonce]; !ok {
			known[d.Nonce] = d
		}
	}

	var appliedN, waitedN, claimedN int
	counted := make(map[uint64]struct{}, len(ordered))
	mark := func(nonce uint64, n *int) {
		if _, ok := counted[nonce]; ok {
			return
		}
		counted[nonce] = struct{}{}
		*n++
	}
	flush := func() {
		observability.AddChallengeDiffs("applied", appliedN)
		observability.AddChallengeDiffs("waited", waitedN)
		observability.AddChallengeDiffs("claimed", claimedN)
		appliedN, waitedN, claimedN = 0, 0, 0
	}

	var waits, gapRetries int
	var lastErr error
	for {
		if err := ctx.Err(); err != nil {
			flush()
			return nil, err
		}
		latest := h.sm.LatestNonce()
		for _, w := range ordered {
			if w.Nonce <= latest {
				mark(w.Nonce, &appliedN)
			}
		}
		overlap := h.claimsOverlapping(ordered, latest)
		if len(overlap) > 0 {
			if waits >= challengeClaimAttempts {
				flush()
				if lastErr == nil {
					lastErr = errors.New("challenge claim attempts exhausted")
				}
				return nil, lastErr
			}
			before := latest
			h.mu.Unlock()
			err := waitClaims(ctx, overlap)
			h.mu.Lock()
			if err != nil {
				flush()
				return nil, err
			}
			failed := false
			for _, c := range overlap {
				if c.err != nil {
					failed = true
					lastErr = c.err
				}
			}
			if failed {
				observability.IncChallengeClaimRetry("claim_failed")
			}
			// Count a wait that applied, and a failed claim that is still
			// listed: the latter would otherwise spin this loop. A failed
			// claim that has left the list did not apply a nonce.
			if !failed || failedClaimStillListed(h.writer.claims, overlap) {
				waits++
			}
			now := h.sm.LatestNonce()
			for _, w := range ordered {
				if w.Nonce > before && w.Nonce <= now {
					mark(w.Nonce, &waitedN)
				}
			}
			continue
		}

		run, below, holeNonce := claimableRun(ordered, latest, h.writer.claims)
		if holeNonce > 0 {
			flush()
			return nil, fmt.Errorf("%w: escrow %s at %d, request needs %d", ErrReconcileGap, h.escrowID, latest, holeNonce)
		}
		if len(run) == 0 {
			flush()
			return nil, nil
		}

		claim := &nonceClaim{
			from:   run[0].Nonce,
			to:     run[len(run)-1].Nonce,
			done:   make(chan struct{}),
			queued: make(chan struct{}),
		}
		h.writer.claims = append(h.writer.claims, claim)
		// Decode and check signatures off the host lock. The claim is already
		// published, so another request waits or stacks instead of decoding
		// the same nonces. A bad signature stops the run there.
		h.mu.Unlock()
		decoded, decErr, sigErr := screenClaim(ctx, h, run, known)
		h.mu.Lock()
		for _, d := range decoded {
			mark(d.Nonce, &claimedN)
		}
		if decErr != nil {
			h.endClaimLocked(claim, decErr)
			flush()
			return nil, decErr
		}
		// A cancel during the signature check must not queue the batch. The
		// claim ends here, and a waiter applies the nonces from its own bytes.
		if err := ctx.Err(); err != nil {
			h.endClaimLocked(claim, err)
			flush()
			return nil, err
		}
		for _, d := range decoded {
			if err := h.checkDiffNonceLimitLocked(d); err != nil {
				h.endClaimLocked(claim, err)
				flush()
				return nil, err
			}
		}
		if len(decoded) == 0 {
			err := sigErr
			if err == nil {
				err = errors.New("empty challenge claim")
			}
			h.endClaimLocked(claim, err)
			flush()
			return nil, err
		}
		claim.from = decoded[0].Nonce
		claim.to = decoded[len(decoded)-1].Nonce
		// Wait until the claim below this one is in the writer queue, not until
		// it has been applied. The writer takes the queue in order and one batch
		// at a time, so a batch queued first is applied first, and two batches
		// that reach the queue together still share one round. The cap keeps a
		// slow lower claim from turning this request into a deadline.
		if below != nil {
			h.mu.Unlock()
			if testBeforeStackedWait != nil {
				testBeforeStackedWait()
			}
			waitCtx, cancelWait := stackedWaitContext(ctx)
			select {
			case <-below.queued:
			case <-below.done:
			case <-waitCtx.Done():
			}
			cancelWait()
			h.mu.Lock()
			if err := ctx.Err(); err != nil {
				h.endClaimLocked(claim, err)
				flush()
				return nil, err
			}
		}
		newly, err := h.enqueueDiffsLocked(ctx, decoded, claim.queued)
		if err == nil {
			err = sigErr
		}
		h.endClaimLocked(claim, err)
		// Retry when the claim below did not land, whatever error the writer
		// reported. A shared batch returns that error rather than ErrReconcileGap.
		if below != nil && gapRetries < 1 && h.sm.LatestNonce()+1 < claim.from {
			gapRetries++
			observability.IncChallengeClaimRetry("gap")
			lastErr = err
			for _, d := range decoded {
				if _, ok := known[d.Nonce]; !ok {
					known[d.Nonce] = d
				}
			}
			continue
		}
		// A hole stops this run early. The prefix is applied; the next pass
		// reports the gap instead of returning success for the page.
		if err == nil && h.sm.LatestNonce() >= claim.to {
			continue
		}
		flush()
		return newly, err
	}
}

func failedClaimStillListed(active, waited []*nonceClaim) bool {
	for _, c := range waited {
		if c.err != nil && slices.Contains(active, c) {
			return true
		}
	}
	return false
}

func (h *Host) claimsOverlapping(wires []types.DiffWire, latest uint64) []*nonceClaim {
	var out []*nonceClaim
	seen := map[*nonceClaim]struct{}{}
	for _, w := range wires {
		if w.Nonce <= latest {
			continue
		}
		for _, c := range h.writer.claims {
			if w.Nonce < c.from || w.Nonce > c.to {
				continue
			}
			if _, ok := seen[c]; ok {
				continue
			}
			seen[c] = struct{}{}
			out = append(out, c)
		}
	}
	return out
}

func claimableRun(wires []types.DiffWire, latest uint64, claims []*nonceClaim) (run []types.DiffWire, below *nonceClaim, holeNonce uint64) {
	var missing []types.DiffWire
	var prev uint64
	for _, w := range wires {
		if w.Nonce <= latest || w.Nonce == prev {
			continue
		}
		prev = w.Nonce
		missing = append(missing, w)
	}
	if len(missing) == 0 {
		return nil, nil, 0
	}
	start := missing[0].Nonce
	if start != latest+1 {
		// Keep the claim itself. A signature failure later shrinks its to, and a
		// search for "the claim that ends at start-1" would no longer find it.
		below = claimBelow(start, claims)
		if below == nil {
			return nil, nil, start
		}
	}
	run = append(run, missing[0])
	for _, w := range missing[1:] {
		if w.Nonce != run[len(run)-1].Nonce+1 || claimCovers(w.Nonce, claims) {
			break
		}
		run = append(run, w)
	}
	return run, below, 0
}

func claimCovers(nonce uint64, claims []*nonceClaim) bool {
	for _, c := range claims {
		if nonce >= c.from && nonce <= c.to {
			return true
		}
	}
	return false
}

func claimBelow(nonce uint64, claims []*nonceClaim) *nonceClaim {
	for _, c := range claims {
		if c.to+1 == nonce {
			return c
		}
	}
	return nil
}

// stackedWaitContext bounds a stacked claim's wait for the claim below it.
// Half the remaining deadline leaves time to enqueue and return an answered
// gap error instead of the request's own deadline.
func stackedWaitContext(parent context.Context) (context.Context, context.CancelFunc) {
	deadline, ok := parent.Deadline()
	if !ok {
		return context.WithCancel(parent)
	}
	remaining := time.Until(deadline)
	limit := remaining / 2
	if testStackedWaitLimit != nil {
		limit = testStackedWaitLimit(remaining)
	}
	if limit >= remaining && remaining > 0 {
		limit = remaining / 2
	}
	if limit <= 0 {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		return ctx, cancel
	}
	return context.WithTimeout(parent, limit)
}

// screenClaim decodes a claimed run one diff at a time. Each user signature is
// checked before the next wire is decoded. decoded is the prefix that passed.
// decErr means the run was not usable, and the caller applies nothing. sigErr
// means the following wire failed its signature; the caller queues decoded.
func screenClaim(ctx context.Context, h *Host, run []types.DiffWire, known map[uint64]types.Diff) (decoded []types.Diff, decErr, sigErr error) {
	decoded = make([]types.Diff, 0, len(run))
	for _, w := range run {
		if err := ctx.Err(); err != nil {
			return decoded, err, nil
		}
		d, ok := known[w.Nonce]
		if !ok {
			if testBeforeClaimDecode != nil {
				testBeforeClaimDecode()
			}
			var err error
			d, err = types.DecodeDiffWire(w)
			if err != nil {
				return decoded, fmt.Errorf("diff %d: %w", w.Nonce, err), nil
			}
		}
		if testBeforeClaimSig != nil {
			testBeforeClaimSig()
		}
		if err := h.sm.VerifyDiffUserSig(d); err != nil {
			return decoded, nil, fmt.Errorf("validate diff nonce %d: %w", d.Nonce, err)
		}
		decoded = append(decoded, d)
	}
	return decoded, nil, nil
}

func waitClaims(ctx context.Context, claims []*nonceClaim) error {
	if testBeforeClaimWait != nil {
		testBeforeClaimWait()
	}
	for _, c := range claims {
		select {
		case <-c.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func (h *Host) endClaimLocked(c *nonceClaim, err error) {
	if c == nil || c.ended {
		return
	}
	c.ended = true
	c.err = err
	h.writer.claims = slices.DeleteFunc(h.writer.claims, func(x *nonceClaim) bool { return x == c })
	close(c.done)
}

func diffWiresFromDiffs(diffs []types.Diff) ([]types.DiffWire, error) {
	if len(diffs) == 0 {
		return nil, nil
	}
	out := make([]types.DiffWire, len(diffs))
	for i, d := range diffs {
		stored, err := proto.Marshal(&types.DiffContent{Txs: d.Txs})
		if err != nil {
			return nil, fmt.Errorf("encode diff %d: %w", d.Nonce, err)
		}
		out[i] = types.DiffWire{
			Nonce:         d.Nonce,
			Txs:           stored,
			UserSig:       d.UserSig,
			PostStateRoot: d.PostStateRoot,
		}
	}
	return out, nil
}
