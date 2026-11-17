package main

import (
	"context"
	"time"

	"devshard/types"
	"devshard/user"
)

// emptyStreamVerdictWait is the fallback grace after vote collection can
// finish. It covers composing/sending the challenge diff and its result
// callback. Collection itself is anchored to the receipt's block time plus
// the configured execution budget, fetch window and vote slack; it can exceed
// one hour. Early verdicts instead live only until their attempt is sampled
// or its race finishes.
const emptyStreamVerdictWait = time.Hour

// emptyStreamStrikeDelay must not expire a penalty while voters can still
// return an honest verdict. The session supplies the protocol's collection
// deadline, including any applicable queued confirmation. An expired deadline
// keeps the original fallback grace. Subtract only after adding grace as a
// Time, so a far-future clock value saturates instead of overflowing a Duration
// into an immediate timeout.
func emptyStreamStrikeDelay(now, collectionDeadline time.Time) time.Duration {
	base := now
	if collectionDeadline.After(base) {
		base = collectionDeadline
	}
	return base.Add(emptyStreamVerdictWait).Sub(now)
}

// emptyStreamStrike holds a sampled empty stream until its challenge ends.
type emptyStreamStrike struct {
	ctx            context.Context
	participantKey string
	stats          QuarantinePayloadStats
	// received holds the stream's hashes, judged against the Finish the
	// challenge reports (emptyStreamVerdict).
	received [][32]byte
	timer    *time.Timer
}

// An early verdict retains only the Finish hashes and outcome, never the
// signed votes or response payload. Its owner is an active local attempt.
type earlyEmptyStreamVerdict struct {
	result  user.ChallengeResult
	outcome string
}

func (e *Redundancy) trackEmptyStreamAttempt(inf *inflight) {
	if inf == nil || inf.probe || e.session == nil || !types.ProtocolKnowsChallenge(e.session.StateMachine().ProtocolVersion()) {
		return
	}
	e.emptyStrikesMu.Lock()
	defer e.emptyStrikesMu.Unlock()
	if e.stopped.Load() {
		return
	}
	if e.emptyStrikeAttempts == nil {
		e.emptyStrikeAttempts = make(map[uint64]*earlyEmptyStreamVerdict)
	}
	if _, known := e.emptyStrikeAttempts[inf.nonce]; !known {
		e.emptyStrikeAttempts[inf.nonce] = nil
	}
}

func (e *Redundancy) forgetEmptyStreamAttempt(nonce uint64) {
	e.emptyStrikesMu.Lock()
	delete(e.emptyStrikeAttempts, nonce)
	e.emptyStrikesMu.Unlock()
}

func (e *Redundancy) forgetEmptyStreamAttempts(attempts []*inflight) {
	e.emptyStrikesMu.Lock()
	defer e.emptyStrikesMu.Unlock()
	for _, inf := range attempts {
		if inf != nil {
			delete(e.emptyStrikeAttempts, inf.nonce)
		}
	}
}

// holdEmptyStreamStrike leaves the strike of an empty stream to its
// challenge. It reports false when no challenge will judge the stream; the
// caller then strikes at once, as before the challenge existed.
//
// A stream that ran to its Finish on a session that runs the usage challenge
// is challenged as EMPTY_STREAM once the Finish applies, and the voters count
// the generated positions of the stored body. A stream can end empty
// honestly: a client's stop string, logit_bias on a lone byte, or </think>
// then EOS leave no text and still have tokens. Only the stored body tells
// that apart from a host that generated nothing, so the strike waits for the
// voters.
//
// The voters judge the stored body against the hashes of the applied Finish,
// not what reached the gateway, and an empty stream is challenged instead of
// put to the served binding. So only a stream that hashes to the Finish the
// session holds is held: one cut on the way, or not the answer the executor
// signed for, is struck as before, whatever the voters find. A host's
// response can list a Finish the session dropped, and a queued Finish can be
// replaced before it applies, so the verdict clears the strike only when the
// stream also hashes to the Finish the voters judged.
func (e *Redundancy) holdEmptyStreamStrike(inf *inflight, participantKey string, stats QuarantinePayloadStats) bool {
	if e.session == nil || inf.resp == nil || !types.ProtocolKnowsChallenge(e.session.StateMachine().ProtocolVersion()) {
		return false
	}
	received := inf.resp.ReceivedResponseHashes
	nonce, ctx := inf.nonce, inf.attemptCtx()
	now := time.Now()
	collectionDeadline, bound := e.session.ReceivedStreamChallengeDeadline(nonce, received, now)
	if !bound {
		return false
	}
	wait := emptyStreamStrikeDelay(now, collectionDeadline)
	e.emptyStrikesMu.Lock()
	if _, held := e.emptyStrikes[nonce]; held {
		e.emptyStrikesMu.Unlock()
		return true
	}
	early := e.emptyStrikeAttempts[nonce]
	delete(e.emptyStrikeAttempts, nonce)
	if early != nil {
		e.emptyStrikesMu.Unlock()
		e.applyEmptyStreamStrike(ctx, nonce, participantKey, stats, emptyStreamVerdict(early.result, early.outcome, received))
		return true
	}
	if e.stopped.Load() {
		// Stop joins the finalizers after marking the gateway stopped. Honor
		// verdicts they already own above, but do not start another wait.
		e.emptyStrikesMu.Unlock()
		return false
	}
	strike := &emptyStreamStrike{ctx: ctx, participantKey: participantKey, stats: stats, received: received}
	strike.timer = time.AfterFunc(wait, func() { e.expireEmptyStreamStrike(nonce, strike) })
	if e.emptyStrikes == nil {
		e.emptyStrikes = make(map[uint64]*emptyStreamStrike)
	}
	e.emptyStrikes[nonce] = strike
	e.emptyStrikesMu.Unlock()
	logInferenceStage(ctx, e.devshardID, nonce, "empty_stream_strike_held", "participant", participantKey)
	return true
}

// emptyStreamVerdict is the outcome that settles a strike on a stream with
// the received hashes. "none" clears the strike only when the stream hashes
// to the Finish the voters judged; otherwise the verdict is about another
// answer, and the outcome is "none_unbound".
func emptyStreamVerdict(result user.ChallengeResult, outcome string, received [][32]byte) string {
	if outcome == "none" && !result.BindsReceivedStream(received) {
		return "none_unbound"
	}
	return outcome
}

// settleEmptyStreamStrike takes the outcome of a finished challenge
// (challengeOutcome) to the strike of the stream it judged. Any challenge can
// end before sampling: in particular STREAM_UNBOUND can judge an honest empty
// stream while it is still open. Keep that outcome only for a tracked attempt,
// rather than retaining every unbound stream (including completed race losers)
// for an hour. Sampling or race cleanup removes this entry deterministically.
func (e *Redundancy) settleEmptyStreamStrike(result user.ChallengeResult, outcome string) {
	nonce := result.Nonce
	e.emptyStrikesMu.Lock()
	strike, known := e.emptyStrikes[nonce]
	if !known {
		// A registered finalizer may still be joining after Stop starts. Its
		// result remains useful until sampling or Stop's final map cleanup;
		// stopped forbids new registrations, not these existing callbacks.
		if early, tracked := e.emptyStrikeAttempts[nonce]; tracked && early == nil {
			e.emptyStrikeAttempts[nonce] = &earlyEmptyStreamVerdict{
				result: user.ChallengeResult{
					ResponseHash: copyFinishHash(result.ResponseHash),
					ServedHash:   copyFinishHash(result.ServedHash),
				},
				outcome: outcome,
			}
		}
		e.emptyStrikesMu.Unlock()
		return
	}
	delete(e.emptyStrikes, nonce)
	e.emptyStrikesMu.Unlock()
	strike.timer.Stop()
	e.applyEmptyStreamStrike(strike.ctx, nonce, strike.participantKey, strike.stats, emptyStreamVerdict(result, outcome, strike.received))
}

func copyFinishHash(hash []byte) []byte {
	if len(hash) != 32 {
		return nil
	}
	return append([]byte(nil), hash...)
}

// expireEmptyStreamStrike applies a strike still waiting for its verdict.
func (e *Redundancy) expireEmptyStreamStrike(nonce uint64, strike *emptyStreamStrike) {
	e.emptyStrikesMu.Lock()
	if e.emptyStrikes[nonce] != strike {
		e.emptyStrikesMu.Unlock()
		return
	}
	delete(e.emptyStrikes, nonce)
	e.emptyStrikesMu.Unlock()
	e.applyEmptyStreamStrike(strike.ctx, nonce, strike.participantKey, strike.stats, "expired")
}

// settleEmptyStreamStrikes applies every strike still held. Stop calls it
// once the race cleanups are joined, so no challenge is left to judge them.
func (e *Redundancy) settleEmptyStreamStrikes() {
	e.emptyStrikesMu.Lock()
	strikes := e.emptyStrikes
	e.emptyStrikes = nil
	e.emptyStrikeAttempts = nil
	e.emptyStrikesMu.Unlock()
	for nonce, strike := range strikes {
		strike.timer.Stop()
		e.applyEmptyStreamStrike(strike.ctx, nonce, strike.participantKey, strike.stats, "stopped")
	}
}

// applyEmptyStreamStrike strikes the participant unless the challenge found
// the stored body sound. Only "none" clears it: the voters found no fault,
// and the hosts that did not answer could not have changed that. An
// invalidated inference, a fault below the threshold, and a challenge that
// could not judge the stream (no votes, failed, incomplete, skipped), or
// judged a Finish this stream does not hash to (none_unbound), keep the
// strike the gateway applied before the challenge existed.
func (e *Redundancy) applyEmptyStreamStrike(ctx context.Context, nonce uint64, participantKey string, stats QuarantinePayloadStats, outcome string) {
	struck := outcome != "none"
	logInferenceStage(ctx, e.devshardID, nonce, "empty_stream_strike_settled",
		"participant", participantKey, "outcome", outcome, "strike", struck)
	if struck && e.participantLimiter != nil {
		e.participantLimiter.ObserveEmptyStreamForModelWithStats(participantKey, e.model, stats)
	}
}
