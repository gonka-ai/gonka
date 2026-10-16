package host

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"

	"devshard/logging"
	"devshard/memwatch"
	"devshard/observability"
	"devshard/storage"
	"devshard/types"
)

var errMissingRefusalJournal = errors.New("missing refusal diff storage")

// RefusalProgressRetries is how often a nonce that keeps advancing can postpone the timeout vote.
// Past this, the vote is accepted even if the executor would move again.
const RefusalProgressRetries = 3

// RefusalVerifyBudget is one refused-timeout check. It is shorter than the
// gateway's 3 minute VerifyTimeout, so the verifier returns before the
// gateway gives up on the vote.
const RefusalVerifyBudget = 2 * time.Minute

// executorCallError is an executor ChallengeReceipt failure. A journal error
// or the walk's own deadline is returned unwrapped so the caller does not
// accept the timeout.
type executorCallError struct{ err error }

// catchUpCallError is a nil-payload catch-up failure. The payload was not sent.
type catchUpCallError struct{ err error }

func (e *catchUpCallError) Error() string { return e.err.Error() }
func (e *catchUpCallError) Unwrap() error { return e.err }

func (e *executorCallError) Error() string { return e.err.Error() }
func (e *executorCallError) Unwrap() error { return e.err }

// VerifyRefusedProgress is the verifier's refused-timeout check.
// Deadline, mempool, and payload checks do not contact the executor; a rejection is final.
// SessionHead nonce T selects diffs from T+1, for any root and for a tip past LatestNonce.
// session not found starts at nonce 1. Any other answered SessionHead error sends the
// payload and no diffs. No HTTP response accepts the timeout. An answer with no verified
// receipt rechecks the tip only when the previous root matched, and challenges from the
// new tip, at most RefusalProgressRetries times.
// WithColdWalk, on this verifier only, keeps one gated walk in flight per executor: a
// cold head (session not found or tip 0), or any suffix past coldWalkLongNonces. Other
// gated checks wait and do not read the journal. The next gated walk resumes at the
// nonce already sent. Once that nonce is past LatestNonce, or coldCatchUpAttempts walks
// ended without progress, a gated check sends the payload alone and is judged as usual.
// Progress is a root-matched tip above the highest seen; it clears the memory. A
// cancelled check does not spend an attempt.
// The walk's own deadline, and a tip read that hits its own query deadline, are returned.
// They do not accept the timeout. An answered catch-up error still sends the payload
// before that answer is classified. A per-page tip moves the cursor only when its root
// matches the journal.
// unreachable reports that a probe got no HTTP response. A nil classifier treats every error
// as no response. queryTimeout and verifyBudget bound the tip read and the whole check.
func VerifyRefusedProgress(
	ctx context.Context,
	st types.EscrowState,
	inferenceID uint64,
	rec *types.InferenceRecord,
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
	localAccept, localErr := verifyRefusedTimeout(ctx, st, inferenceID, rec, payload, localMempool, nil, nil, nil, ev, st.Config, nowUnix)
	if localErr != nil || !localAccept {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		return localAccept, localErr
	}
	if tip == nil || executorClient == nil || st.LatestNonce == 0 {
		accept, err := verifyRefusedTimeout(ctx, st, inferenceID, rec, payload, localMempool, nil, executorClient, ingest, ev, st.Config, nowUnix)
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
		verifyBudget = RefusalVerifyBudget
	}
	ctx, cancel := context.WithTimeout(ctx, verifyBudget)
	defer cancel()

	for {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		deadline, _ := ctx.Deadline()
		budget := time.Until(deadline)
		queryBudget := min(queryTimeout, budget/4)
		if queryBudget <= 0 {
			if err := ctx.Err(); err != nil {
				return false, err
			}
			return false, context.DeadlineExceeded
		}
		queryCtx, queryCancel := context.WithTimeout(ctx, queryBudget)
		nonce, root, queryErr := tip.SessionHead(queryCtx)
		probeErr := queryCtx.Err()
		queryCancel()
		if queryErr != nil && requestCtx.Err() != nil && errors.Is(queryErr, requestCtx.Err()) {
			return false, requestCtx.Err()
		}
		if queryErr != nil && ctx.Err() != nil && errors.Is(queryErr, ctx.Err()) {
			return false, ctx.Err()
		}
		if ownProbeTimeout(ctx, probeErr, queryErr) {
			return false, probeErr
		}

		cursor, matched, early, earlyErr := refusalCursor(st, nonce, root, queryErr, journal, unreachable)
		if earlyErr != nil {
			return false, earlyErr
		}
		if early {
			return true, nil
		}

		walkCtx := ctx
		if walk := coldWalkFrom(ctx); walk != nil {
			join, resume, joinErr := walk.gate.Join(ctx, walk.key, refusalWalkHead(st, cursor, nonce, matched, queryErr), st.LatestNonce)
			if joinErr != nil {
				return false, joinErr
			}
			switch join {
			case coldRetry:
				continue
			case coldPayloadOnly:
				cursor = st.LatestNonce + 1
			case coldLead:
				if resume > cursor {
					cursor = resume
				}
				lead := &coldWalkLead{gate: walk.gate, key: walk.key, request: requestCtx}
				defer lead.release()
				walkCtx = context.WithValue(ctx, coldLeadKey{}, lead)
			}
		}

		return challengeAndMaybeRetry(walkCtx, st, inferenceID, rec, payload, cursor, matched, nonce, executorClient, ingest, ev, tip, journal, queryTimeout, unreachable)
	}
}

// refusalCursor chooses the first nonce to send. early means accept without a challenge.
func refusalCursor(st types.EscrowState, nonce uint64, root []byte, queryErr error, journal RefusalJournal, unreachable func(error) bool) (cursor uint64, matched bool, early bool, err error) {
	if queryErr != nil {
		if unreachable(queryErr) {
			return 0, false, true, nil
		}
		if sessionNotFound(queryErr) {
			logRefusalCursor("cold", 1, nonce, st.LatestNonce)
			return 1, false, false, nil
		}
		logRefusalCursor("ahead", st.LatestNonce+1, nonce, st.LatestNonce)
		return st.LatestNonce + 1, false, false, nil
	}
	if nonce > 0 && nonce <= st.LatestNonce && len(root) == 32 {
		matched, err = journal.Anchor(nonce, root)
		if err != nil {
			return 0, false, false, err
		}
	}
	cursor = nonce + 1
	choice := "tip"
	if nonce > st.LatestNonce {
		choice = "ahead"
	}
	logRefusalCursor(choice, cursor, nonce, st.LatestNonce)
	return cursor, matched, false, nil
}

// refusalWalkHead describes the walk the first SessionHead asks for. A tip
// past LatestNonce and an unanswered head walk nothing, so they are not long.
func refusalWalkHead(st types.EscrowState, cursor, nonce uint64, matched bool, queryErr error) walkHead {
	h := walkHead{cold: sessionNotFound(queryErr) || (queryErr == nil && nonce == 0)}
	if queryErr != nil || h.cold {
		return h
	}
	if matched {
		h.matchedTip = nonce
	}
	h.long = cursor <= st.LatestNonce && st.LatestNonce-cursor+1 > coldWalkLongNonces
	return h
}

func logRefusalCursor(choice string, cursor, tip, latest uint64) {
	logging.Debug("refusal cursor", "choice", choice, "cursor", cursor, "tip_nonce", tip, "latest", latest)
}

// sessionNotFound reports that SessionHead answered "this escrow is not here".
// Connect NotFound is that answer. Any other error matches only when its own
// text is exactly "session not found", so a 500 body that mentions the phrase
// does not start a cold walk.
func sessionNotFound(err error) bool {
	if err == nil {
		return false
	}
	var ce *connect.Error
	if errors.As(err, &ce) {
		return ce.Code() == connect.CodeNotFound
	}
	if errors.Is(err, storage.ErrSessionNotFound) {
		return true
	}
	for err != nil {
		if err.Error() == "session not found" {
			return true
		}
		next := errors.Unwrap(err)
		if next == nil {
			return false
		}
		err = next
	}
	return false
}

// ownProbeTimeout reports that err is the deadline of a tip read whose parent
// context is still running. That is the verifier's query budget, not a dead executor.
func ownProbeTimeout(parent context.Context, probeErr, err error) bool {
	return err != nil && parent.Err() == nil && probeErr != nil && errors.Is(err, probeErr)
}

func challengeAndMaybeRetry(
	ctx context.Context,
	st types.EscrowState,
	inferenceID uint64,
	rec *types.InferenceRecord,
	payload *InferencePayload,
	cursor uint64,
	matched bool,
	previous uint64,
	executorClient ExecutorClient,
	ingest TxSink,
	ev EvidenceVerifier,
	tip SessionTip,
	journal RefusalJournal,
	queryTimeout time.Duration,
	unreachable func(error) bool,
) (bool, error) {
	outcome, err := challengeOnce(ctx, st, inferenceID, rec, payload, cursor, executorClient, ingest, ev, tip, journal, queryTimeout, unreachable)
	accept, done, err := classifyChallenge(outcome, err, unreachable)
	if done {
		return accept, err
	}
	if !matched {
		return true, nil
	}
	return refusalAfterProgress(ctx, st, inferenceID, rec, payload, executorClient, ingest, ev, tip, journal, previous, queryTimeout, unreachable)
}

// classifyChallenge turns a challengeFrom result into an accept decision.
// done is false when the caller may retry a matching root.
func classifyChallenge(outcome refusedChallenge, err error, unreachable func(error) bool) (accept bool, done bool, ret error) {
	if err != nil {
		var call *executorCallError
		if !errors.As(err, &call) {
			return false, true, err
		}
		if unreachable(call.err) {
			return true, true, nil
		}
		return false, false, nil
	}
	if outcome == refusedReceipt {
		return false, true, nil
	}
	return false, false, nil
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
	unreachable func(error) bool,
) (bool, error) {
	for range RefusalProgressRetries {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		deadline, _ := ctx.Deadline()
		queryBudget := min(queryTimeout, time.Until(deadline)/4)
		if queryBudget <= 0 {
			return false, context.DeadlineExceeded
		}
		queryCtx, queryCancel := context.WithTimeout(ctx, queryBudget)
		nonce, root, queryErr := tip.SessionHead(queryCtx)
		probeErr := queryCtx.Err()
		queryCancel()
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		if ownProbeTimeout(ctx, probeErr, queryErr) {
			return false, probeErr
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
		outcome, err := challengeOnce(ctx, st, inferenceID, rec, payload, nonce+1, executorClient, ingest, ev, tip, journal, queryTimeout, unreachable)
		accept, done, err := classifyChallenge(outcome, err, unreachable)
		if !done {
			previous = nonce
			continue
		}
		return accept, err
	}
	return true, nil
}

// challengeOnce walks from the cursor. An answered catch-up error still sends
// the payload before the caller classifies the challenge.
func challengeOnce(
	ctx context.Context,
	st types.EscrowState,
	inferenceID uint64,
	rec *types.InferenceRecord,
	payload *InferencePayload,
	from uint64,
	executorClient ExecutorClient,
	ingest TxSink,
	ev EvidenceVerifier,
	tip SessionTip,
	journal RefusalJournal,
	queryTimeout time.Duration,
	unreachable func(error) bool,
) (refusedChallenge, error) {
	outcome, err := challengeFrom(ctx, st, inferenceID, rec, payload, from, executorClient, ingest, ev, tip, journal, queryTimeout, unreachable)
	var catchUp *catchUpCallError
	if !errors.As(err, &catchUp) {
		return outcome, err
	}
	releaseColdWalk(ctx)
	if unreachable(catchUp.err) {
		return 0, &executorCallError{err: catchUp.err}
	}
	return finishPayload(ctx, st.EscrowID, inferenceID, rec, payload, nil, executorClient, ingest, ev, unreachable)
}

// challengeFrom sends [from, LatestNonce] one page at a time, then the payload.
// The last page rides with the payload when it fits, unless this check leads a
// gated walk; the leader sends every page as catch-up and releases the gate
// before the payload call. Each page is sent before the next one is loaded.
// Catch-up pages use a child deadline that leaves a reserve for that payload call.
func challengeFrom(
	ctx context.Context,
	st types.EscrowState,
	inferenceID uint64,
	rec *types.InferenceRecord,
	payload *InferencePayload,
	from uint64,
	executorClient ExecutorClient,
	ingest TxSink,
	ev EvidenceVerifier,
	tip SessionTip,
	journal RefusalJournal,
	queryTimeout time.Duration,
	unreachable func(error) bool,
) (refusedChallenge, error) {
	if from > st.LatestNonce {
		releaseColdWalk(ctx)
		return finishPayload(ctx, st.EscrowID, inferenceID, rec, payload, nil, executorClient, ingest, ev, unreachable)
	}
	walkCtx, walkCancel := walkContext(ctx, queryTimeout)
	defer walkCancel()

	var last []types.DiffWire
	err := journal.Pages(walkCtx, from, st.LatestNonce, func() uint64 {
		noteColdCursor(walkCtx, from)
		return from
	}, func(page []types.DiffWire, more bool) (func(context.Context) error, error) {
		if err := walkCtx.Err(); err != nil {
			return nil, err
		}
		warnOversizedDiff(page)
		if more {
			return prepareCatchUp(walkCtx, inferenceID, &from, page, executorClient, tip, journal, st.LatestNonce, queryTimeout, unreachable)
		}
		last = append([]types.DiffWire(nil), page...)
		return nil, nil
	})
	if err != nil {
		return 0, err
	}
	last = wiresFromCursor(last, from)
	if coldLeadFrom(ctx) == nil && pageFitsPayload(last, payload) {
		decoded, err := decodeWires(last)
		last = nil
		if err != nil {
			return 0, err
		}
		return finishPayload(ctx, st.EscrowID, inferenceID, rec, payload, decoded, executorClient, ingest, ev, unreachable)
	}
	if len(last) > 0 {
		send, err := prepareCatchUp(walkCtx, inferenceID, &from, last, executorClient, tip, journal, st.LatestNonce, queryTimeout, unreachable)
		last = nil
		if err != nil {
			return 0, err
		}
		if send != nil {
			if err := send(walkCtx); err != nil {
				return 0, err
			}
			noteColdCursor(walkCtx, from)
		}
	}
	releaseColdWalk(ctx)
	return finishPayload(ctx, st.EscrowID, inferenceID, rec, payload, nil, executorClient, ingest, ev, unreachable)
}

// challengeCatchUpPreparer copies a page into the request body. The returned
// send func does not reference the page, so the caller can drop it before the
// request goes out.
type challengeCatchUpPreparer interface {
	PrepareChallengeCatchUp(inferenceID uint64, diffs []types.DiffWire, protocolVersion string) (func(context.Context) error, error)
}

// prepareCatchUp checks the stored bytes and builds the catch-up send.
// A diff that is not well-framed is returned as-is, so a damaged journal row
// does not become an answered executor error. The send func does not retain
// page. protocolVersion is empty; a cold executor reads it from the diffs.
func prepareCatchUp(
	walkCtx context.Context,
	inferenceID uint64,
	cursor *uint64,
	page []types.DiffWire,
	executor ExecutorClient,
	tip SessionTip,
	journal RefusalJournal,
	latest uint64,
	queryTimeout time.Duration,
	unreachable func(error) bool,
) (func(context.Context) error, error) {
	page = wiresFromCursor(page, *cursor)
	if len(page) == 0 {
		return nil, nil
	}
	if err := walkCtx.Err(); err != nil {
		return nil, err
	}
	if err := screenCatchUpPage(page); err != nil {
		return nil, err
	}
	send, err := catchUpSender(executor, inferenceID, page)
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context) error {
		err := send(ctx)
		noteRefusalPage(err, ctx, unreachable)
		if err != nil {
			if ctx.Err() != nil && errors.Is(err, ctx.Err()) {
				return ctx.Err()
			}
			return &catchUpCallError{err: err}
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		nonce, root, tipErr := readTip(ctx, tip, queryTimeout)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if tipErr == nil && tipRootMatches(journal, latest, nonce, *cursor, root) {
			*cursor = nonce + 1
			noteColdBound(ctx, nonce)
		}
		// The tip stays put on a cold executor, so the next unread nonce is
		// the one after this page unless the tip jumped further.
		advanced := *cursor
		if len(page) > 0 && page[len(page)-1].Nonce+1 > advanced {
			advanced = page[len(page)-1].Nonce + 1
		}
		noteColdCursor(ctx, advanced)
		return nil
	}, nil
}

func catchUpSender(executor ExecutorClient, inferenceID uint64, page []types.DiffWire) (func(context.Context) error, error) {
	if prep, ok := executor.(challengeCatchUpPreparer); ok {
		return prep.PrepareChallengeCatchUp(inferenceID, page, "")
	}
	kept := append([]types.DiffWire(nil), page...)
	return func(ctx context.Context) error {
		return executor.ChallengeCatchUp(ctx, inferenceID, kept, "")
	}, nil
}

// screenCatchUpPage checks each stored blob's framing. One damaged diff fails
// the check before the page is sent. The walk does not decode transactions.
func screenCatchUpPage(page []types.DiffWire) error {
	for _, w := range page {
		if err := types.CheckDiffContentFraming(w.Txs); err != nil {
			return fmt.Errorf("diff %d: %w", w.Nonce, err)
		}
	}
	return nil
}

// tipRootMatches reports that nonce is a stored journal row whose root is root.
// A tip past latest, or a root that does not match, does not move the cursor.
func tipRootMatches(journal RefusalJournal, latest, nonce, cursor uint64, root []byte) bool {
	if journal == nil || nonce+1 <= cursor || nonce > latest || len(root) != 32 {
		return false
	}
	ok, err := journal.Anchor(nonce, root)
	return err == nil && ok
}

func finishPayload(
	ctx context.Context,
	escrowID string,
	inferenceID uint64,
	rec *types.InferenceRecord,
	payload *InferencePayload,
	diffs []types.Diff,
	executor ExecutorClient,
	ingest TxSink,
	ev EvidenceVerifier,
	unreachable func(error) bool,
) (refusedChallenge, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	ctx = memwatch.WithChallengeStats(ctx)
	outcome, err := challengeRefused(ctx, escrowID, inferenceID, rec, payload, diffs, executor, ingest, ev)
	noteRefusalPage(err, nil, unreachable)
	if err != nil {
		if ctx.Err() != nil && errors.Is(err, ctx.Err()) {
			return 0, ctx.Err()
		}
		return 0, &executorCallError{err: err}
	}
	return outcome, nil
}

func warnOversizedDiff(page []types.DiffWire) {
	if len(page) == 1 && wirePageCost(page) > ChallengePageMaxBytes {
		logging.Warn("refusal diff over page budget", "nonce", page[0].Nonce, "stored_bytes", len(page[0].Txs))
	}
}

func noteRefusalPage(err error, walkCtx context.Context, unreachable func(error) bool) {
	observability.IncRefusalChallengePage(refusalPageResult(err, walkCtx, unreachable))
}

func refusalPageResult(err error, walkCtx context.Context, unreachable func(error) bool) string {
	if err == nil {
		return "ok"
	}
	if walkCtx != nil && walkCtx.Err() != nil && errors.Is(err, walkCtx.Err()) {
		return "deadline"
	}
	if refusalMessageTooLarge(err) {
		return "too_large"
	}
	if unreachable != nil && unreachable(err) {
		return "unreachable"
	}
	return "answered_error"
}

// refusalMessageTooLarge matches transport.isConnectMessageTooLarge. This
// package cannot import transport.
func refusalMessageTooLarge(err error) bool {
	if connect.CodeOf(err) != connect.CodeResourceExhausted {
		return false
	}
	msg := err.Error()
	var ce *connect.Error
	if errors.As(err, &ce) {
		msg = ce.Message()
	}
	return strings.Contains(msg, "larger than configured max") ||
		strings.Contains(msg, "exceeds sendMaxBytes") ||
		strings.Contains(msg, "exceeds getURLMaxBytes") ||
		strings.Contains(msg, "http.MaxBytesReader") ||
		strings.Contains(msg, "bandwidth exhausted") ||
		strings.Contains(msg, "attach request too large")
}

func walkContext(ctx context.Context, queryTimeout time.Duration) (context.Context, context.CancelFunc) {
	deadline, ok := ctx.Deadline()
	if !ok {
		return ctx, func() {}
	}
	reserve := queryTimeout
	if reserve <= 0 {
		reserve = 30 * time.Second
	}
	if quarter := time.Until(deadline) / 4; quarter < reserve {
		reserve = quarter
	}
	if reserve <= 0 {
		return ctx, func() {}
	}
	return context.WithDeadline(ctx, deadline.Add(-reserve))
}

func readTip(ctx context.Context, tip SessionTip, queryTimeout time.Duration) (uint64, []byte, error) {
	budget := queryTimeout
	if budget <= 0 {
		budget = 30 * time.Second
	}
	if deadline, ok := ctx.Deadline(); ok {
		if remain := time.Until(deadline); remain < budget {
			budget = remain
		}
	}
	if budget <= 0 {
		if err := ctx.Err(); err != nil {
			return 0, nil, err
		}
		return 0, nil, context.DeadlineExceeded
	}
	queryCtx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	return tip.SessionHead(queryCtx)
}

func wiresFromCursor(page []types.DiffWire, cursor uint64) []types.DiffWire {
	i := 0
	for i < len(page) && page[i].Nonce < cursor {
		i++
	}
	if i == 0 {
		return page
	}
	if i >= len(page) {
		return nil
	}
	return page[i:]
}

func decodeWires(page []types.DiffWire) ([]types.Diff, error) {
	if len(page) == 0 {
		return nil, nil
	}
	out := make([]types.Diff, len(page))
	for i, w := range page {
		d, err := types.DecodeDiffWire(w)
		if err != nil {
			return nil, err
		}
		out[i] = d
	}
	return out, nil
}
