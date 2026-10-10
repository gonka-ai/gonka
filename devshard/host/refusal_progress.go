package host

import (
	"context"
	"errors"
	"time"

	"devshard/types"
)

var errMissingRefusalJournal = errors.New("missing refusal diff storage")

// RefusalProgressRetries is how often a nonce that keeps advancing can postpone the timeout vote.
// Past this, the vote is accepted even if the executor would move again.
const RefusalProgressRetries = 3

// VerifyRefusedProgress is the verifier's refused-timeout check.
// Deadline, mempool, and payload checks do not contact the executor; a rejection is final.
// A client that reports its applied tip is challenged with the diffs after that tip once the
// stored state hash matches. No HTTP response accepts the timeout. An answer with no verified
// receipt rechecks the tip and, when it advanced and still matches, challenges from the new
// tip only, at most RefusalProgressRetries times. A tip that cannot be verified falls back
// to one challenge carrying the diffs from nonce 1.
// unreachable reports that a probe got no HTTP response. A nil classifier treats every error
// as no response. queryTimeout and verifyBudget bound the tip read and the whole check.
func VerifyRefusedProgress(
	ctx context.Context,
	st types.EscrowState,
	inferenceID uint64,
	payload *InferencePayload,
	localMempool []*types.DevshardTx,
	executorClient ExecutorClient,
	ingest TxSink,
	ev EvidenceVerifier,
	tip SessionTip,
	journal RefusalJournal,
	queryTimeout time.Duration,
	verifyBudget time.Duration,
	nowUnix int64,
	unreachable func(error) bool,
) (bool, error) {
	requestCtx := ctx
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if unreachable == nil {
		unreachable = func(error) bool { return true }
	}
	localAccept, localErr := VerifyRefusedTimeout(ctx, st, inferenceID, payload, localMempool, nil, nil, ev, st.Config, nowUnix)
	if localErr != nil || !localAccept {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		return localAccept, localErr
	}
	if tip == nil || executorClient == nil || st.LatestNonce == 0 {
		accept, err := VerifyRefusedTimeout(ctx, st, inferenceID, payload, localMempool, executorClient, ingest, ev, st.Config, nowUnix)
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		return accept, err
	}
	if journal == nil {
		return false, errMissingRefusalJournal
	}
	if queryTimeout <= 0 {
		queryTimeout = 30 * time.Second
	}
	if verifyBudget <= 0 {
		verifyBudget = 3 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, verifyBudget)
	defer cancel()
	load := func(from uint64) ([]types.Diff, error) {
		if from > st.LatestNonce {
			return nil, nil
		}
		return journal.Diffs(from, st.LatestNonce)
	}
	deadline, _ := ctx.Deadline()
	budget := time.Until(deadline)
	queryBudget := min(queryTimeout, budget/4)
	queryCtx, queryCancel := context.WithTimeout(ctx, queryBudget)
	nonce, root, queryErr := tip.SessionHead(queryCtx)
	queryCancel()
	rec := st.Inferences[inferenceID]
	if queryErr != nil {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		if unreachable(queryErr) {
			return true, nil
		}
	} else if nonce > 0 && nonce <= st.LatestNonce && len(root) == 32 {
		match, err := journal.Anchor(nonce, root)
		if err != nil {
			return false, err
		}
		if match {
			var diffs []types.Diff
			if nonce < st.LatestNonce {
				diffs, err = load(nonce + 1)
				if err != nil {
					return false, err
				}
			}
			// No response accepts the timeout. An answer with no verified receipt
			// rechecks the nonce instead of sending the history from nonce 1.
			shortCtx, shortCancel := context.WithTimeout(ctx, budget/4)
			outcome, challengeErr := challengeRefused(shortCtx, st.EscrowID, inferenceID, rec, payload, diffs, executorClient, ingest, ev)
			shortCancel()
			if ctx.Err() != nil {
				return false, ctx.Err()
			}
			if challengeErr == nil && outcome == refusedReceipt {
				return false, nil
			}
			if challengeErr != nil && unreachable(challengeErr) {
				return true, nil
			}
			return refusalAfterProgress(ctx, st, inferenceID, rec, payload, executorClient, ingest, ev, tip, journal, nonce, queryTimeout, load, unreachable)
		}
	}
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	diffs, err := load(1)
	if err != nil {
		return false, err
	}
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	accept, err := verifyRefusedTimeout(ctx, st, inferenceID, payload, localMempool, diffs, executorClient, ingest, ev, st.Config, nowUnix)
	if requestCtx.Err() != nil {
		return false, requestCtx.Err()
	}
	return accept, err
}

// refusalAfterProgress runs after a challenge answered without a verified receipt.
// A nonce that did not advance, or a new nonce whose root does not match, accepts the timeout.
// A higher nonce with a matching root is challenged from that nonce, at most RefusalProgressRetries times.
func refusalAfterProgress(
	ctx context.Context,
	st types.EscrowState,
	inferenceID uint64,
	rec *types.InferenceRecord,
	payload *InferencePayload,
	executorClient ExecutorClient,
	ingest TxSink,
	ev EvidenceVerifier,
	tip SessionTip,
	journal RefusalJournal,
	previous uint64,
	queryTimeout time.Duration,
	load func(uint64) ([]types.Diff, error),
	unreachable func(error) bool,
) (bool, error) {
	for range RefusalProgressRetries {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		deadline, _ := ctx.Deadline()
		queryBudget := min(queryTimeout, time.Until(deadline)/4)
		if queryBudget <= 0 {
			return true, nil
		}
		queryCtx, queryCancel := context.WithTimeout(ctx, queryBudget)
		nonce, root, queryErr := tip.SessionHead(queryCtx)
		queryCancel()
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		if queryErr != nil || nonce <= previous || nonce > st.LatestNonce || len(root) != 32 {
			return true, nil
		}
		match, err := journal.Anchor(nonce, root)
		if err != nil {
			return false, err
		}
		if !match {
			return true, nil
		}
		var diffs []types.Diff
		if nonce < st.LatestNonce {
			diffs, err = load(nonce + 1)
			if err != nil {
				return false, err
			}
		}
		challengeCtx, challengeCancel := context.WithTimeout(ctx, time.Until(deadline)/2)
		outcome, challengeErr := challengeRefused(challengeCtx, st.EscrowID, inferenceID, rec, payload, diffs, executorClient, ingest, ev)
		challengeCancel()
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		if challengeErr == nil && outcome == refusedReceipt {
			return false, nil
		}
		if challengeErr != nil && unreachable(challengeErr) {
			return true, nil
		}
		previous = nonce
	}
	return true, nil
}
