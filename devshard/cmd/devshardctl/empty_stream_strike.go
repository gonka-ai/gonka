package main

import (
	"bytes"
	"context"
	"time"

	"devshard/types"
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
	// outcome is set by a challenge that ended before the strike was held.
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
// The voters judge the stored body against the hashes the Finish signs, not
// what reached the gateway, and an empty stream is challenged instead of put
// to the served binding. So only a stream that hashes to its Finish is held:
// one cut on the way, or not the answer the executor signed for, is struck as
// before, whatever the voters find.
func (e *Redundancy) holdEmptyStreamStrike(inf *inflight, participantKey string, stats QuarantinePayloadStats) bool {
	if e.session == nil || !types.ProtocolKnowsChallenge(e.session.StateMachine().ProtocolVersion()) ||
		!e.session.IsNonceFinished(inf.nonce) || !receivedStreamBindsFinish(inf) {
		return false
	}
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
		e.applyEmptyStreamStrike(ctx, nonce, participantKey, stats, strike.outcome)
		return true
	}
	strike := &emptyStreamStrike{held: true, ctx: ctx, participantKey: participantKey, stats: stats}
	strike.timer = time.AfterFunc(emptyStreamVerdictWait, func() { e.expireEmptyStreamStrike(nonce, strike) })
	if e.emptyStrikes == nil {
		e.emptyStrikes = make(map[uint64]*emptyStreamStrike)
	}
	e.emptyStrikes[nonce] = strike
	e.emptyStrikesMu.Unlock()
	logInferenceStage(ctx, e.devshardID, nonce, "empty_stream_strike_held", "participant", participantKey)
	return true
}

// receivedStreamBindsFinish reports whether the stream the gateway received
// hashes to the response or served hash of the Finish it ended with.
func receivedStreamBindsFinish(inf *inflight) bool {
	if inf.resp == nil {
		return false
	}
	for _, tx := range inf.resp.Mempool {
		finish := tx.GetFinishInference()
		if finish == nil || finish.InferenceId != inf.nonce {
			continue
		}
		for _, sum := range inf.resp.ReceivedResponseHashes {
			if bytes.Equal(sum[:], finish.ResponseHash) || bytes.Equal(sum[:], finish.ServedHash) {
				return true
			}
		}
	}
	return false
}

// settleEmptyStreamStrike takes the outcome of a finished challenge
// (challengeOutcome) to the strike of the stream it judged. An EMPTY_STREAM
// challenge can end before its strike is held; the outcome then waits for it.
func (e *Redundancy) settleEmptyStreamStrike(nonce uint64, reason types.ChallengeReason, outcome string) {
	e.emptyStrikesMu.Lock()
	strike, known := e.emptyStrikes[nonce]
	if !known {
		if reason == types.ChallengeReason_CHALLENGE_REASON_EMPTY_STREAM && !e.stopped.Load() {
			early := &emptyStreamStrike{outcome: outcome}
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
	e.applyEmptyStreamStrike(strike.ctx, nonce, strike.participantKey, strike.stats, outcome)
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
// could not judge the stream (no votes, failed, incomplete, skipped) keep the
// strike the gateway applied before the challenge existed.
func (e *Redundancy) applyEmptyStreamStrike(ctx context.Context, nonce uint64, participantKey string, stats QuarantinePayloadStats, outcome string) {
	struck := outcome != "none"
	logInferenceStage(ctx, e.devshardID, nonce, "empty_stream_strike_settled",
		"participant", participantKey, "outcome", outcome, "strike", struck)
	if struck && e.participantLimiter != nil {
		e.participantLimiter.ObserveEmptyStreamForModelWithStats(participantKey, e.model, stats)
	}
}
