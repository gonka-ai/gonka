package main

import (
	"context"
	"errors"

	"devshard/types"
	"devshard/user"
)

func (e *Redundancy) handleServedBinding(nonce uint64, hostIndex int, participantKey string, verdict user.ServedBinding) {
	if e.metrics != nil {
		e.metrics.RecordServedBinding(string(verdict))
	}
	if verdict != user.ServedBindingMismatch {
		return
	}
	e.servedBindingStrikes.Add(1)
	go func() {
		defer e.servedBindingStrikes.Done()
		e.strikeServedBindingMismatch(nonce, hostIndex, participantKey)
	}()
}

func (e *Redundancy) strikeServedBindingMismatch(nonce uint64, hostIndex int, participantKey string) {
	if participantKey == "" {
		participantKey = legacyHostPerfKey(hostIndex)
	}
	logInferenceWarn(context.Background(), e.devshardID, nonce, "served_binding_failed", "participant", participantKey)
	if e.perf == nil {
		return
	}
	e.recordFailureSample(RequestSample{
		HostIdx:        hostIndex,
		ParticipantKey: participantKey,
		Model:          normalizeModelID(e.model),
		Responsive:     false,
	})
	if e.participantLimiter != nil && e.perf.ParticipantFailureThresholdExceeded(participantKey) {
		e.participantLimiter.ObserveStalledWinner(participantKey)
	}
}

func bindsReceivedStream(attempt *inflight) bool {
	return attempt != nil && attempt.resp != nil && !attempt.probe && !attempt.phaseTransitionAborted
}

func (e *Redundancy) recordFailureSample(sample RequestSample) {
	e.perf.Record(sample)
	if e.metrics != nil {
		e.metrics.ObserveRequestSample(sample)
	}
}

// challengeStreamReason is the challenge an attempt's stream calls for once
// its Finish applies. Probes, phase-change aborts and streams the gateway cut
// itself before any receipt are never challenged: no Finish follows them. A
// cut the gateway made after the receipt is challenged like any other: the
// gateway cancels a race loser or a stalled winner in answer to how the
// executor streamed, and the voters judge the stored body, not the cut; an
// honest executor that finished the work after the cut (as it does since the
// execution outlives the request) keeps a body that votes NONE. A stream that
// reached [DONE] or devshard_meta is judged by the served binding instead, so
// only an empty one is marked here. A signed receipt with no content after it
// is EMPTY_STREAM whether or not the stream was cut: the voters then count
// the positions of the stored body, which a cut on the way to the gateway
// does not change. The exception is the reasoning model whose empty content
// with reported completion tokens is a documented outcome (isModelBurnEmpty):
// as the gateway does not quarantine the host for it, it does not challenge
// it either, and the voters count no positions on that body as no fault.
func challengeStreamReason(attempt *inflight, gatewayCut bool, model string) (types.ChallengeReason, bool) {
	if attempt == nil || attempt.probe || attempt.phaseTransitionAborted || (gatewayCut && !attempt.hasReceipt()) {
		return types.ChallengeReason_CHALLENGE_REASON_UNSPECIFIED, false
	}
	empty := errors.Is(attempt.err, errEmptyStream) || (attempt.hasReceipt() && attempt.contentChunks.Load() == 0)
	if empty && !(types.ModelBurnsEmpty(model) && attempt.usageComplTokens.Load() > 0) {
		return types.ChallengeReason_CHALLENGE_REASON_EMPTY_STREAM, true
	}
	if attempt.resp == nil || !attempt.resp.ReceivedComplete {
		return types.ChallengeReason_CHALLENGE_REASON_STREAM_UNBOUND, true
	}
	return types.ChallengeReason_CHALLENGE_REASON_UNSPECIFIED, false
}

// phaseTransitionCut is the side-effect-free part of markPhaseTransitionAbort
// for a failed send: the attempt started before PoC generation and PoC is now
// on, so the executor dropping the stream is the phase change, not a cut.
func phaseTransitionCut(attempt *inflight) bool {
	return attempt.startedBeforePoCGeneration && currentPoCGenerationActive()
}

// streamClose is how an attempt's stream is closed on the session once the
// attempt ends, whether the send succeeded or failed.
type streamClose int

const (
	// streamCloseChallenge marks the stream for a challenge once its Finish
	// applies.
	streamCloseChallenge streamClose = iota
	// streamCloseBind hands the received hashes to the served binding, which
	// judges them against the Finish.
	streamCloseBind
	// streamCloseEnded closes the stream with nothing to check: a probe, a
	// phase-change abort, a cut the gateway made before any receipt.
	streamCloseEnded
)

// attemptStreamClose decides how an attempt's stream is closed. A failed
// attempt whose stream still ran to devshard_meta is bound like a successful
// one: left open it would be challenged as outliving the grace.
func attemptStreamClose(attempt *inflight, gatewayCut bool, model string) (streamClose, types.ChallengeReason) {
	if reason, ok := challengeStreamReason(attempt, gatewayCut, model); ok {
		return streamCloseChallenge, reason
	}
	if bindsReceivedStream(attempt) && len(attempt.resp.ReceivedResponseHashes) > 0 {
		return streamCloseBind, types.ChallengeReason_CHALLENGE_REASON_UNSPECIFIED
	}
	return streamCloseEnded, types.ChallengeReason_CHALLENGE_REASON_UNSPECIFIED
}

// closeAttemptStream closes on the session the stream the attempt opened.
func (e *Redundancy) closeAttemptStream(attempt *inflight, gatewayCut bool) {
	if e.session == nil || attempt == nil {
		return
	}
	switch close, reason := attemptStreamClose(attempt, gatewayCut, e.model); close {
	case streamCloseChallenge:
		e.session.MarkChallengeStream(attempt.nonce, reason)
	case streamCloseBind:
		e.session.BindReceivedStream(attempt.nonce, attempt.resp.ReceivedResponseHashes)
	default:
		e.session.StreamEnded(attempt.nonce)
	}
}

// handleChallenge runs a challenge under the settle barrier, so the escrow is
// not settled while the votes are still being collected or sent.
func (e *Redundancy) handleChallenge(nonce uint64, reason types.ChallengeReason) {
	if e.stopped.Load() || e.session == nil {
		return
	}
	e.goTrackedRaceCleanup(context.Background(), func(ctx context.Context) {
		result, err := e.session.RunChallenge(ctx, nonce, reason)
		e.recordChallengeResult(result, err)
	})
}

func (e *Redundancy) recordChallengeResult(result user.ChallengeResult, err error) {
	if err != nil {
		logInferenceWarn(context.Background(), e.devshardID, result.Nonce, "challenge_failed",
			"reason", types.ChallengeReasonLabel(result.Reason), "error", err)
	} else {
		logInferenceStage(context.Background(), e.devshardID, result.Nonce, "challenge_finished",
			"reason", types.ChallengeReasonLabel(result.Reason), "outcome", challengeOutcome(result, err),
			"votes", len(result.Votes), "fault_weight", result.Faults, "threshold", result.Threshold,
			"unanswered", result.Unanswered, "unanswered_weight", result.UnansweredWeight)
	}
	if e.metrics != nil {
		participant := "unknown"
		if result.ExecutorIdx >= 0 {
			participant = e.participantKeyForHost(result.ExecutorIdx)
		}
		e.metrics.RecordChallenge(types.ChallengeReasonLabel(result.Reason), participant, challengeOutcome(result, err))
	}
}

// challengeOutcome labels a finished challenge. "none" means the votes found
// no fault in the stored body and the hosts that did not answer could not
// have changed that: an honest cut. "incomplete" is no fault either, but
// with enough hosts unanswered that their votes alone could have invalidated
// the record: the check did not happen.
func challengeOutcome(result user.ChallengeResult, err error) string {
	switch {
	case result.Invalidated:
		return "invalidated"
	case err != nil:
		return "failed"
	case result.UnsupportedProtocol:
		return "unsupported_protocol"
	case result.NonceCap:
		return "nonce_cap"
	case result.ReplayFirst:
		return "replay_first"
	case len(result.Votes) == 0:
		return "no_votes"
	case result.Faults > 0:
		return "below_threshold"
	case result.UnansweredWeight > result.Threshold:
		return "incomplete"
	default:
		return "none"
	}
}
