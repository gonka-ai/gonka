package main

import (
	"context"
	"time"

	"devshard/types"
	"devshard/user"
)

// emptyStreamVerdictWait bounds how long a held empty-stream strike waits for
// its challenge, and how long a challenge outcome that came first waits for
// its strike. The Finish applies with the next diff, at the latest at the
// quiet settle (5 minutes), and the hosts then fetch the stored body for up to
// the challenge window (30 minutes) plus the vote slack (2 minutes). A strike
// still held after that is applied.
var emptyStreamVerdictWait = time.Hour

// emptyStreamStrike pairs the strike of an empty stream with the outcome of
// the challenge that judges it. Whichever arrives first waits for the other.
type emptyStreamStrike struct {
	// held is set by the strike, with the attempt's log context, the
	// participant that takes it and its payload stats.
	held           bool
	ctx            context.Context
	participantKey string
	stats          QuarantinePayloadStats
	// received holds the stream's hashes, judged against the Finish the
	// challenge reports (emptyStreamVerdict).
	received [][32]byte
	// result and outcome are set by a challenge that ended before the strike
	// was held.
	result  user.ChallengeResult
	outcome string
	timer   *time.Timer
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
	if e.session == nil || inf.resp == nil || !types.ProtocolKnowsChallenge(e.session.StateMachine().ProtocolVersion()) ||
		!e.session.ReceivedStreamBindsFinish(inf.nonce, inf.resp.ReceivedResponseHashes) {
		return false
	}
	received := inf.resp.ReceivedResponseHashes
	nonce, ctx := inf.nonce, inf.attemptCtx()
	e.emptyStrikesMu.Lock()
	if e.stopped.Load() {
		// Stop has settled the strikes, and no challenge runs any more.
		e.emptyStrikesMu.Unlock()
		return false
	}
	if strike, known := e.emptyStrikes[nonce]; known {
		if strike.held {
			e.emptyStrikesMu.Unlock()
			return true
		}
		// The challenge ended first.
		delete(e.emptyStrikes, nonce)
		e.emptyStrikesMu.Unlock()
		strike.timer.Stop()
		e.applyEmptyStreamStrike(ctx, nonce, participantKey, stats, emptyStreamVerdict(strike.result, strike.outcome, received))
		return true
	}
	strike := &emptyStreamStrike{held: true, ctx: ctx, participantKey: participantKey, stats: stats, received: received}
	strike.timer = time.AfterFunc(emptyStreamVerdictWait, func() { e.expireEmptyStreamStrike(nonce, strike) })
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
// (challengeOutcome) to the strike of the stream it judged. An EMPTY_STREAM
// challenge can end before its strike is held; the outcome then waits for it.
func (e *Redundancy) settleEmptyStreamStrike(result user.ChallengeResult, outcome string) {
	nonce := result.Nonce
	e.emptyStrikesMu.Lock()
	strike, known := e.emptyStrikes[nonce]
	if !known {
		if result.Reason == types.ChallengeReason_CHALLENGE_REASON_EMPTY_STREAM && !e.stopped.Load() {
			early := &emptyStreamStrike{result: result, outcome: outcome}
			early.timer = time.AfterFunc(emptyStreamVerdictWait, func() { e.expireEmptyStreamStrike(nonce, early) })
			if e.emptyStrikes == nil {
				e.emptyStrikes = make(map[uint64]*emptyStreamStrike)
			}
			e.emptyStrikes[nonce] = early
		}
		e.emptyStrikesMu.Unlock()
		return
	}
	if !strike.held {
		// The first outcome stands.
		e.emptyStrikesMu.Unlock()
		return
	}
	delete(e.emptyStrikes, nonce)
	e.emptyStrikesMu.Unlock()
	strike.timer.Stop()
	e.applyEmptyStreamStrike(strike.ctx, nonce, strike.participantKey, strike.stats, emptyStreamVerdict(result, outcome, strike.received))
}

// expireEmptyStreamStrike ends the wait of strike: a strike still held is
// applied, an outcome no strike claimed is dropped.
func (e *Redundancy) expireEmptyStreamStrike(nonce uint64, strike *emptyStreamStrike) {
	e.emptyStrikesMu.Lock()
	if e.emptyStrikes[nonce] != strike {
		e.emptyStrikesMu.Unlock()
		return
	}
	delete(e.emptyStrikes, nonce)
	e.emptyStrikesMu.Unlock()
	if strike.held {
		e.applyEmptyStreamStrike(strike.ctx, nonce, strike.participantKey, strike.stats, "expired")
	}
}

// settleEmptyStreamStrikes applies every strike still held. Stop calls it
// once the race cleanups are joined, so no challenge is left to judge them.
func (e *Redundancy) settleEmptyStreamStrikes() {
	e.emptyStrikesMu.Lock()
	strikes := e.emptyStrikes
	e.emptyStrikes = nil
	e.emptyStrikesMu.Unlock()
	for nonce, strike := range strikes {
		strike.timer.Stop()
		if strike.held {
			e.applyEmptyStreamStrike(strike.ctx, nonce, strike.participantKey, strike.stats, "stopped")
		}
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
