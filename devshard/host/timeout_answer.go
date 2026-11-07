package host

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"time"

	"devshard/chainoracle/blocks/headertime"
	"devshard/logging"
	"devshard/state"
	"devshard/types"
)

// verifierMap is this process's wall clock and stored header times.
// A verifier without a stored feed keeps the TimeoutDue gate (header mode)
// or the wall Due check (legacy mode).
type verifierMap interface {
	VerifierNow() time.Time
	HasHeaderFeed() bool
	StampHeaderTime(height uint64, hash []byte) (int64, bool)
	TimeoutInterval(reason types.TimeoutReason) time.Duration
	HeightsyncTimeoutClock() bool
}

// storedHeaderTime is the map read that does not call the filler.
type storedHeaderTime interface {
	StoredAt(height uint64) (headertime.Record, bool)
}

// ErrDuplicateChallenge is a second VerifyTimeout for an inference and reason
// this process is already answering or has already voted without a stored
// signature yet. ServeVerifyTimeout replays LookupTimeoutVote when present.
var ErrDuplicateChallenge = errors.New("timeout challenge already held")

// ErrAnswerBusy is returned when too many verifier checks are already in
// flight on this host. ServeVerifyTimeout answers accept=false with no vote.
var ErrAnswerBusy = errors.New("timeout answer capacity reached")

// maxConcurrentAnswers caps in-flight (held) verifier checks per host session.
// Voted/remembered entries do not count. Each check may hold a goroutine and
// map watcher for up to about AnswerWait (~60s).
const maxConcurrentAnswers = 32

// deferralKey is one inference and one timeout reason.
type deferralKey struct {
	inferenceID uint64
	reason      types.TimeoutReason
}

// answerMark is the host-local memory for one (inference, reason).
// A restart drops it. It is not part of the state machine or the diff.
type answerMark uint8

const (
	answerHeld answerMark = iota + 1
	answerVoted
)

// answerEntry is one check: held while answering, then voted with an optional
// signed payload so a gateway retry can replay the same vote.
type answerEntry struct {
	mark      answerMark
	signature []byte
	voterSlot uint32
}

// answerHolds is one check per inference and reason. A held check is the
// schedule timer, the feed wait, or the executor call. A voted check has
// already returned a vote. ignore, feed_behind, and accept=false are not kept.
type answerHolds struct {
	mu       sync.Mutex
	held     map[deferralKey]answerEntry
	mismatch map[deferralKey]struct{}
}

func (a *answerHolds) tryHold(inferenceID uint64, reason types.TimeoutReason) (func(bool), error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.held == nil {
		a.held = make(map[deferralKey]answerEntry)
	}
	key := deferralKey{inferenceID: inferenceID, reason: reason}
	if entry, ok := a.held[key]; ok {
		warnDuplicateChallenge(inferenceID, reason, entry.mark == answerVoted)
		return nil, ErrDuplicateChallenge
	}
	held := 0
	for _, e := range a.held {
		if e.mark == answerHeld {
			held++
		}
	}
	if held >= maxConcurrentAnswers {
		return nil, ErrAnswerBusy
	}
	a.held[key] = answerEntry{mark: answerHeld}
	return func(voted bool) {
		a.mu.Lock()
		defer a.mu.Unlock()
		cur, ok := a.held[key]
		if !ok {
			return
		}
		if voted {
			cur.mark = answerVoted
			a.held[key] = cur
			return
		}
		if cur.mark == answerHeld {
			delete(a.held, key)
		}
	}, nil
}

func (a *answerHolds) rememberVote(inferenceID uint64, reason types.TimeoutReason, signature []byte, voterSlot uint32) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.held == nil {
		a.held = make(map[deferralKey]answerEntry)
	}
	key := deferralKey{inferenceID: inferenceID, reason: reason}
	cur := a.held[key]
	cur.mark = answerVoted
	cur.signature = append([]byte(nil), signature...)
	cur.voterSlot = voterSlot
	a.held[key] = cur
}

func (a *answerHolds) lookupVote(inferenceID uint64, reason types.TimeoutReason) (signature []byte, voterSlot uint32, ok bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	cur, found := a.held[deferralKey{inferenceID: inferenceID, reason: reason}]
	if !found || cur.mark != answerVoted || len(cur.signature) == 0 {
		return nil, 0, false
	}
	return append([]byte(nil), cur.signature...), cur.voterSlot, true
}

// claimClockMismatch reports the first wall/stamp mismatch for this inference
// and reason. A later retry of the same pair does not count again.
func (a *answerHolds) claimClockMismatch(inferenceID uint64, reason types.TimeoutReason) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.mismatch == nil {
		a.mismatch = make(map[deferralKey]struct{})
	}
	key := deferralKey{inferenceID: inferenceID, reason: reason}
	if _, seen := a.mismatch[key]; seen {
		return false
	}
	a.mismatch[key] = struct{}{}
	return true
}

func (a *answerHolds) forget(inferenceID uint64, reason types.TimeoutReason) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.held == nil {
		return
	}
	delete(a.held, deferralKey{inferenceID: inferenceID, reason: reason})
}

// forgetInference drops held and voted entries for both timeout reasons.
// Used when a record leaves the live map (sealed or drained).
func (a *answerHolds) forgetInference(inferenceID uint64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, reason := range []types.TimeoutReason{
		types.TimeoutReason_TIMEOUT_REASON_REFUSED,
		types.TimeoutReason_TIMEOUT_REASON_EXECUTION,
	} {
		key := deferralKey{inferenceID: inferenceID, reason: reason}
		delete(a.held, key)
		delete(a.mismatch, key)
	}
}

func warnDuplicateChallenge(inferenceID uint64, reason types.TimeoutReason, voted bool) {
	msg := "timeout challenge already held"
	if voted {
		msg = "timeout challenge already answered"
	}
	logging.Warn(msg,
		"subsystem", "host",
		"inference", inferenceID,
		"reason", reason.String(),
	)
}

func (h *Host) VerifierNow() time.Time {
	if h != nil && h.answerNow != nil {
		return h.answerNow()
	}
	return time.Now()
}

func (h *Host) HasHeaderFeed() bool {
	_, ok := h.storedHeaders()
	return ok
}

func (h *Host) StampHeaderTime(height uint64, hash []byte) (int64, bool) {
	src, ok := h.storedHeaders()
	if !ok {
		return 0, false
	}
	return storedStampTime(src, height, hash)
}

func (h *Host) TimeoutInterval(reason types.TimeoutReason) time.Duration {
	if h == nil || h.sm == nil {
		return 0
	}
	return timeoutIntervalOf(h.sm.Config(), reason)
}

// claimClockMismatch is the once-per (inference, reason) guard for the
// verifier mismatch counter. A nil host records every call.
func (h *Host) claimClockMismatch(inferenceID uint64, reason types.TimeoutReason) bool {
	if h == nil {
		return true
	}
	return h.answerHolds.claimClockMismatch(inferenceID, reason)
}

func (h *Host) HeightsyncTimeoutClock() bool {
	if h == nil || h.sm == nil {
		return false
	}
	return h.sm.Config().HeightsyncTimeoutClock
}

func (h *Host) escrowIDForMismatch() string {
	if h == nil {
		return ""
	}
	return h.escrowID
}

func (h *Host) After(d time.Duration) <-chan time.Time {
	if d < 0 {
		d = 0
	}
	if h != nil && h.answerAfter != nil {
		return h.answerAfter(d)
	}
	// Production verifier waits go through waitAnswer (NewTimer+Stop). After
	// remains for injectors and any caller that only has this channel API.
	return time.After(d)
}

func (h *Host) WatchHeaders(fn func()) func() {
	if h == nil || h.sm == nil || fn == nil {
		return func() {}
	}
	w, ok := h.sm.HeaderTimes().(state.HeaderTimeWatch)
	if !ok || w == nil {
		return func() {}
	}
	return w.Watch(fn)
}

func (h *Host) TryHold(inferenceID uint64, reason types.TimeoutReason) (func(bool), error) {
	if h == nil {
		return func(bool) {}, nil
	}
	return h.answerHolds.tryHold(inferenceID, reason)
}

// forgetAnswersLeftLive drops answer memory for inferences that were live in
// before and are gone now (sealed or drained). Caller holds h.mu when the
// live set is read after apply.
func (h *Host) forgetAnswersLeftLive(before map[uint64]struct{}) {
	if h == nil || h.sm == nil || len(before) == 0 {
		return
	}
	after := h.sm.LiveInferenceIDs()
	for id := range before {
		if _, still := after[id]; still {
			continue
		}
		h.answerHolds.forgetInference(id)
	}
}

// ForgetTimeoutVote drops a remembered vote for this inference and reason.
// A restart drops every vote. The next check also drops a vote whose record
// has left the status that reason requires.
func (h *Host) ForgetTimeoutVote(inferenceID uint64, reason types.TimeoutReason) {
	if h == nil {
		return
	}
	h.answerHolds.forget(inferenceID, reason)
}

// RememberTimeoutVote stores the signed accept vote for a gateway retry.
// Only Accept=true votes are stored; a second check is never started.
func (h *Host) RememberTimeoutVote(inferenceID uint64, reason types.TimeoutReason, signature []byte, voterSlot uint32) {
	if h == nil || len(signature) == 0 {
		return
	}
	h.answerHolds.rememberVote(inferenceID, reason, signature, voterSlot)
}

// LookupTimeoutVote returns a previously remembered accept vote, if any.
func (h *Host) LookupTimeoutVote(inferenceID uint64, reason types.TimeoutReason) (signature []byte, voterSlot uint32, ok bool) {
	if h == nil {
		return nil, 0, false
	}
	return h.answerHolds.lookupVote(inferenceID, reason)
}

func (h *Host) storedHeaders() (storedHeaderTime, bool) {
	if h == nil || h.sm == nil {
		return nil, false
	}
	src, ok := h.sm.HeaderTimes().(storedHeaderTime)
	if !ok || src == nil {
		return nil, false
	}
	return src, true
}

func storedStampTime(src storedHeaderTime, height uint64, hash []byte) (int64, bool) {
	if src == nil || height == 0 {
		return 0, false
	}
	row, ok := src.StoredAt(height)
	if !ok || row.TimeMs <= 0 {
		return 0, false
	}
	if len(hash) > 0 && !bytes.Equal(row.Hash, hash) {
		return 0, false
	}
	return row.TimeMs, true
}

func timeoutIntervalOf(cfg types.SessionConfig, reason types.TimeoutReason) time.Duration {
	seconds := cfg.RefusalTimeout
	if reason == types.TimeoutReason_TIMEOUT_REASON_EXECUTION {
		seconds = cfg.ExecutionTimeout
	}
	if seconds <= 0 {
		return 0
	}
	maxSeconds := int64(maxDuration / time.Second)
	if seconds > maxSeconds {
		return maxDuration
	}
	return time.Duration(seconds) * time.Second
}

const maxDuration = time.Duration(int64(^uint64(0) >> 1))

func challengeStamp(rec *types.InferenceRecord, reason types.TimeoutReason) (uint64, []byte) {
	if rec == nil {
		return 0, nil
	}
	if reason == types.TimeoutReason_TIMEOUT_REASON_EXECUTION {
		return rec.ConfirmedAtHeight, rec.ConfirmedAtBlockHash
	}
	return rec.StartedAtHeight, rec.StartedAtBlockHash
}

type answerHeldKey struct{}

// beginAnswer owns the single check for this inference and reason.
// A call that already owns it, such as the refusal progress walk, does not
// take a second one. release(voted) remembers a returned vote and drops any
// other answer. A verifier with no memory of its own always owns the call.
func beginAnswer(ctx context.Context, ev EvidenceVerifier, inferenceID uint64, reason types.TimeoutReason) (context.Context, func(bool), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Value(answerHeldKey{}) != nil {
		return ctx, nil, nil
	}
	release, err := tryAnswerHold(ev, inferenceID, reason)
	if err != nil {
		return ctx, nil, err
	}
	return context.WithValue(ctx, answerHeldKey{}, true), release, nil
}

func finishAnswer(release func(bool), accept bool, err error) {
	if release == nil {
		return
	}
	release(err == nil && accept)
}

// executionStampAnchor is the verifier's execution clock. A carried
// ConfirmedAtTimeMs is that clock. disagree is set when the map has the
// same stamp and a different time. A zero carried time keeps the map row,
// and a miss stays a miss.
func executionStampAnchor(rec *types.InferenceRecord, reason types.TimeoutReason, mapMs int64, found bool) (ms int64, missing, disagree bool) {
	if reason == types.TimeoutReason_TIMEOUT_REASON_EXECUTION && rec != nil && rec.ConfirmedAtTimeMs > 0 {
		return rec.ConfirmedAtTimeMs, false, found && mapMs != rec.ConfirmedAtTimeMs
	}
	return mapMs, !found, false
}

// awaitVerifierDue reports whether the challenge body should run.
// ready is false when the verifier returns without a vote. The caller owns
// the single in-flight check. The 60 second wait is a timer on this call
// (header mode only). It does not call GetBlockHeader and it does not sleep
// inside ChallengeReceipt.
//
// Legacy (HeightsyncTimeoutClock=false): Active is wall StartedAt/ConfirmedAt.
// The stamp is shadowed through CompareLegacyShadow and does not block the
// wall result. There is no AnswerWait on this path.
// Header: existing stamp Decide / schedule / feed_behind behaviour.
func awaitVerifierDue(
	ctx context.Context,
	ev EvidenceVerifier,
	inferenceID uint64,
	rec *types.InferenceRecord,
	reason types.TimeoutReason,
) (bool, error) {
	m, ok := ev.(verifierMap)
	if ok && m != nil && !m.HeightsyncTimeoutClock() {
		return awaitVerifierLegacyDue(ev, m, inferenceID, rec, reason)
	}
	if !ok || m == nil || !m.HasHeaderFeed() {
		if timeoutDue(ev, inferenceID, reason) {
			return true, nil
		}
		return false, nil
	}
	return awaitVerifierStampDue(ctx, ev, m, inferenceID, rec, reason)
}

func wallUnixForReason(rec *types.InferenceRecord, reason types.TimeoutReason) int64 {
	if rec == nil {
		return 0
	}
	if reason == types.TimeoutReason_TIMEOUT_REASON_EXECUTION {
		return rec.ConfirmedAt
	}
	return rec.StartedAt
}

func timeoutKindLabel(reason types.TimeoutReason) string {
	if reason == types.TimeoutReason_TIMEOUT_REASON_EXECUTION {
		return "execution"
	}
	return "refused"
}

func escrowIDOf(ev EvidenceVerifier) string {
	if h, ok := ev.(interface{ escrowIDForMismatch() string }); ok {
		return h.escrowIDForMismatch()
	}
	return ""
}

// awaitVerifierLegacyDue is the devshard-0.2.x-v6 wall check: the challenge
// runs once nowUnix-anchor is at least the timeout in whole seconds. Refusal
// uses StartedAt, execution uses ConfirmedAt. A zero anchor is due. The
// stamp comparison is CompareLegacyShadow, the same function the gateway
// uses, and it never blocks the wall result. There is no AnswerWait and no
// TimeoutBuffer on this check.
func awaitVerifierLegacyDue(
	ev EvidenceVerifier,
	m verifierMap,
	inferenceID uint64,
	rec *types.InferenceRecord,
	reason types.TimeoutReason,
) (bool, error) {
	now := time.Now()
	if m != nil {
		now = m.VerifierNow()
	}
	interval := time.Duration(0)
	if m != nil {
		interval = m.TimeoutInterval(reason)
	} else {
		interval = headertime.AnswerWait
	}
	height, hash := challengeStamp(rec, reason)
	stampMs, found := int64(0), false
	if m != nil && m.HasHeaderFeed() {
		mapMs, mapFound := m.StampHeaderTime(height, hash)
		ms, missing, _ := executionStampAnchor(rec, reason, mapMs, mapFound)
		stampMs, found = ms, !missing
	} else if reason == types.TimeoutReason_TIMEOUT_REASON_EXECUTION && rec != nil && rec.ConfirmedAtTimeMs > 0 {
		stampMs, found = rec.ConfirmedAtTimeMs, true
	}
	wallUnix := wallUnixForReason(rec, reason)
	// A signed anchor of 0 is the Unix epoch and is due once the timeout has
	// elapsed from 1970. The gateway treats a missing sendTime differently:
	// it falls back to StartedAt, and if that is also missing the challenge
	// is unarmed. Do not fold those two zeros together.
	deadline, _, armed := headertime.Due(headertime.ClockDueInput{
		Reason:   timeoutKindLabel(reason),
		Now:      now,
		Wall:     time.Unix(wallUnix, 0),
		Interval: interval,
		Caller:   headertime.CallerVerifier,
	})
	due := armed && !now.Before(deadline)
	shadow := headertime.CompareLegacyShadow(now, time.Unix(wallUnix, 0), deadline, stampMs, !found)
	// A missing stamp is logged once the wall arm is due. A real disagreement
	// is recorded whenever this check runs, including while the wall is ahead.
	if shadow.Cause != "" && (shadow.Mismatch || due) && claimVerifierClockMismatch(ev, inferenceID, reason) {
		headertime.RecordClockMismatch("verifier", timeoutKindLabel(reason), escrowIDOf(ev), inferenceID, headertime.DueEval{
			Active:    headertime.DueSide{Due: due, Wall: true},
			Shadow:    headertime.DueSide{Due: shadow.StampDue},
			HasShadow: true,
			Mismatch:  shadow.Mismatch,
			Cause:     shadow.Cause,
		}, wallUnix, stampMs, height)
	}
	return due, nil
}

// claimVerifierClockMismatch is true the first time this inference and reason
// disagree. Evidence without the guard records every call.
func claimVerifierClockMismatch(ev EvidenceVerifier, inferenceID uint64, reason types.TimeoutReason) bool {
	once, ok := ev.(interface {
		claimClockMismatch(uint64, types.TimeoutReason) bool
	})
	if !ok {
		return true
	}
	return once.claimClockMismatch(inferenceID, reason)
}

func awaitVerifierStampDue(
	ctx context.Context,
	ev EvidenceVerifier,
	m verifierMap,
	inferenceID uint64,
	rec *types.InferenceRecord,
	reason types.TimeoutReason,
) (bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	height, hash := challengeStamp(rec, reason)
	interval := m.TimeoutInterval(reason)
	var waitFrom time.Time
	warned := false
	anchor := func(mapMs int64, found bool) (int64, bool) {
		ms, missing, disagree := executionStampAnchor(rec, reason, mapMs, found)
		if disagree && !warned {
			warned = true
			logging.Warn("claimed confirm time disagrees with the header map; a later dispute rolls back a timeout decided on this time",
				"subsystem", "host",
				"inference", inferenceID,
				"height", height,
				"carried_time_ms", rec.ConfirmedAtTimeMs,
				"map_time_ms", mapMs,
			)
		}
		return ms, missing
	}

	for {
		now := m.VerifierNow()
		mapMs, found := m.StampHeaderTime(height, hash)
		ms, missing := anchor(mapMs, found)
		waited := time.Duration(0)
		if missing {
			if waitFrom.IsZero() {
				waitFrom = now
			}
			waited = now.Sub(waitFrom)
			if waited < 0 {
				waited = 0
			}
		}
		d := headertime.Decide(now, ms, missing, interval, waited, headertime.CallerVerifier)
		switch d.Outcome {
		case headertime.OutcomeDue:
			return true, nil
		case headertime.OutcomeIgnore:
			return false, nil
		case headertime.OutcomeFeedBehind:
			logging.Warn("verifier block feed is behind the chain",
				"subsystem", "host",
				"inference", inferenceID,
				"reason", reason.String(),
			)
			return false, nil
		case headertime.OutcomeSchedule:
			if err := waitAnswer(ctx, ev, d.Delta, nil); err != nil && err != errAnswerTimer {
				return false, err
			}
			now = m.VerifierNow()
			mapMs, found = m.StampHeaderTime(height, hash)
			ms, missing = anchor(mapMs, found)
			again := headertime.Decide(now, ms, missing, interval, 0, headertime.CallerVerifier)
			return again.Outcome == headertime.OutcomeDue, nil
		case headertime.OutcomeWaiting:
			left := headertime.AnswerWait - waited
			if left < 0 {
				left = 0
			}
			notify := make(chan struct{}, 1)
			cancel := watchHeaders(ev, func() {
				select {
				case notify <- struct{}{}:
				default:
				}
			})
			if _, ok := m.StampHeaderTime(height, hash); ok {
				cancel()
				continue
			}
			waitErr := waitAnswer(ctx, ev, left, notify)
			cancel()
			if waitErr != nil && waitErr != errAnswerTimer {
				return false, waitErr
			}
			if waitErr == errAnswerTimer && m.VerifierNow().Sub(waitFrom) < headertime.AnswerWait {
				waitFrom = m.VerifierNow().Add(-headertime.AnswerWait)
			}
		default:
			return false, nil
		}
	}
}

var errAnswerTimer = errAnswerTimerSentinel{}

type errAnswerTimerSentinel struct{}

func (errAnswerTimerSentinel) Error() string { return "verifier answer timer" }

func tryAnswerHold(ev EvidenceVerifier, inferenceID uint64, reason types.TimeoutReason) (func(bool), error) {
	h, ok := ev.(interface {
		TryHold(uint64, types.TimeoutReason) (func(bool), error)
	})
	if !ok || h == nil {
		return func(bool) {}, nil
	}
	return h.TryHold(inferenceID, reason)
}

func forgetAnswer(ev EvidenceVerifier, inferenceID uint64, reason types.TimeoutReason) {
	f, ok := ev.(interface {
		ForgetTimeoutVote(uint64, types.TimeoutReason)
	})
	if !ok || f == nil {
		return
	}
	f.ForgetTimeoutVote(inferenceID, reason)
}

func watchHeaders(ev EvidenceVerifier, fn func()) func() {
	w, ok := ev.(interface {
		WatchHeaders(func()) func()
	})
	if !ok || w == nil {
		return func() {}
	}
	cancel := w.WatchHeaders(fn)
	if cancel == nil {
		return func() {}
	}
	return cancel
}

// answerWaitChannel returns the timer channel for waitAnswer. Production uses
// time.NewTimer so an early return (ctx cancel or map notify) can Stop it.
// Test injectors (*Host.answerAfter or non-Host After) keep ownership of their
// channel and do not get a stop func.
func answerWaitChannel(ev EvidenceVerifier, d time.Duration) (<-chan time.Time, func()) {
	if d < 0 {
		d = 0
	}
	if h, ok := ev.(*Host); ok && h != nil && h.answerAfter != nil {
		return h.answerAfter(d), nil
	}
	if _, isHost := ev.(*Host); !isHost {
		if a, ok := ev.(interface {
			After(time.Duration) <-chan time.Time
		}); ok && a != nil {
			return a.After(d), nil
		}
	}
	t := time.NewTimer(d)
	return t.C, func() { t.Stop() }
}

func waitAnswer(ctx context.Context, ev EvidenceVerifier, d time.Duration, notify <-chan struct{}) error {
	timer, stop := answerWaitChannel(ev, d)
	if stop != nil {
		defer stop()
	}
	if notify == nil {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer:
			return errAnswerTimer
		}
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer:
		return errAnswerTimer
	case <-notify:
		return nil
	}
}
