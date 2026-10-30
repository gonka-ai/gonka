package main

import (
	"errors"
	"testing"

	promtestutil "github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"devshard/host"
	"devshard/types"
	"devshard/user"
)

func TestChallengeOutcomeMetric(t *testing.T) {
	vote := func(fault types.ChallengeFault) *types.ChallengeVote { return &types.ChallengeVote{Fault: fault} }
	for _, testCase := range []struct {
		name   string
		result user.ChallengeResult
		err    error
		want   string
	}{
		{name: "invalidated", result: user.ChallengeResult{Votes: []*types.ChallengeVote{vote(types.ChallengeFault_CHALLENGE_FAULT_HASH_MISMATCH)}, Faults: 2, Invalidated: true}, want: "invalidated"},
		{name: "every vote none", result: user.ChallengeResult{Votes: []*types.ChallengeVote{vote(types.ChallengeFault_CHALLENGE_FAULT_NONE)}}, want: "none"},
		// Mainnet: threshold 8 of 16 slots. Fourteen NONE and one host
		// unanswered is an honest cut; one NONE and fourteen unanswered is
		// a check that did not happen, since the fourteen alone could have
		// invalidated the record.
		{name: "none with one host unanswered", result: user.ChallengeResult{Votes: []*types.ChallengeVote{vote(types.ChallengeFault_CHALLENGE_FAULT_NONE)}, Threshold: 8, Unanswered: 1, UnansweredWeight: 1}, want: "none"},
		{name: "none with most hosts unanswered", result: user.ChallengeResult{Votes: []*types.ChallengeVote{vote(types.ChallengeFault_CHALLENGE_FAULT_NONE)}, Threshold: 8, Unanswered: 14, UnansweredWeight: 14}, want: "incomplete"},
		{name: "none with unanswered weight at the threshold", result: user.ChallengeResult{Votes: []*types.ChallengeVote{vote(types.ChallengeFault_CHALLENGE_FAULT_NONE)}, Threshold: 8, Unanswered: 8, UnansweredWeight: 8}, want: "none"},
		{name: "at the nonce cap", result: user.ChallengeResult{NonceCap: true}, want: "nonce_cap"},
		{name: "faults below threshold", result: user.ChallengeResult{Votes: []*types.ChallengeVote{vote(types.ChallengeFault_CHALLENGE_FAULT_PAYLOAD_UNAVAILABLE)}, Faults: 1}, want: "below_threshold"},
		{name: "no votes", want: "no_votes"},
		{name: "send failed", result: user.ChallengeResult{Votes: []*types.ChallengeVote{vote(types.ChallengeFault_CHALLENGE_FAULT_NONE)}}, err: errors.New("send"), want: "failed"},
		{name: "session of another protocol", result: user.ChallengeResult{UnsupportedProtocol: true}, want: "unsupported_protocol"},
		{name: "replay reached the record first", result: user.ChallengeResult{Votes: []*types.ChallengeVote{vote(types.ChallengeFault_CHALLENGE_FAULT_NONE)}, ReplayFirst: true}, want: "replay_first"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			require.Equal(t, testCase.want, challengeOutcome(testCase.result, testCase.err))
		})
	}

	metrics := NewDevshardMetrics()
	e := &Redundancy{metrics: metrics, devshardID: "1"}
	e.recordChallengeResult(user.ChallengeResult{Reason: types.ChallengeReason_CHALLENGE_REASON_EMPTY_STREAM, ExecutorIdx: 2,
		Votes: []*types.ChallengeVote{vote(types.ChallengeFault_CHALLENGE_FAULT_NONE)}}, nil)
	require.Equal(t, 1.0, promtestutil.ToFloat64(metrics.challenges.WithLabelValues("empty_stream", e.participantKeyForHost(2), "none")))
	// A record the session no longer knows has no executor to charge the
	// outcome to; it must not land on host 0.
	e.recordChallengeResult(user.ChallengeResult{Reason: types.ChallengeReason_CHALLENGE_REASON_STREAM_UNBOUND, ExecutorIdx: -1}, nil)
	require.Equal(t, 1.0, promtestutil.ToFloat64(metrics.challenges.WithLabelValues("stream_unbound", "unknown", "no_votes")))
	require.Equal(t, 0.0, promtestutil.ToFloat64(metrics.challenges.WithLabelValues("stream_unbound", e.participantKeyForHost(0), "no_votes")))
}

func TestChallengeRejectsAreCountedByVoter(t *testing.T) {
	rejects := func(causes ...string) user.ChallengeResult {
		result := user.ChallengeResult{Reason: types.ChallengeReason_CHALLENGE_REASON_OUTPUT_ABOVE_BYTES, ExecutorIdx: 0}
		for _, cause := range causes {
			result.Rejects = append(result.Rejects, user.ChallengeReject{HostIdx: 1, Cause: cause})
		}
		return result
	}

	t.Run("by cause, with a host's own text as other", func(t *testing.T) {
		metrics := NewDevshardMetrics()
		e := &Redundancy{metrics: metrics, devshardID: "1"}
		e.recordChallengeResult(rejects("no_render", "render_busy", "a voter's own words"), nil)
		voter := e.participantKeyForHost(1)
		require.Equal(t, 1.0, promtestutil.ToFloat64(metrics.challengeRejects.WithLabelValues(voter, "no_render")))
		require.Equal(t, 1.0, promtestutil.ToFloat64(metrics.challengeRejects.WithLabelValues(voter, "render_busy")))
		require.Equal(t, 1.0, promtestutil.ToFloat64(metrics.challengeRejects.WithLabelValues(voter, "other")))
	})

	strikeLimit := make([]string, participantFailureStrikeThreshold)
	for i := range strikeLimit {
		strikeLimit[i] = "no_render"
	}
	t.Run("no_render is no strike unless the gateway turns it on", func(t *testing.T) {
		limiter := NewParticipantRequestLimiter(2, 10)
		e := &Redundancy{devshardID: "1", participantLimiter: limiter}
		e.recordChallengeResult(rejects(strikeLimit...), nil)
		require.Zero(t, limiter.Snapshot([]string{e.participantKeyForHost(1)})[e.participantKeyForHost(1)].FailureStrikes)
	})
	t.Run("no_render strikes up to the quarantine", func(t *testing.T) {
		limiter := NewParticipantRequestLimiter(2, 10)
		e := &Redundancy{devshardID: "1", participantLimiter: limiter, noRenderStrike: true}
		voter := e.participantKeyForHost(1)
		e.recordChallengeResult(rejects(strikeLimit[1:]...), nil)
		require.False(t, limiter.IsBlocked(voter))
		require.Equal(t, participantFailureStrikeThreshold-1, limiter.Snapshot([]string{voter})[voter].FailureStrikes)
		e.recordChallengeResult(rejects("no_render"), nil)
		require.True(t, limiter.IsBlocked(voter), "a voter that keeps answering no_render reaches the quarantine")
		require.False(t, limiter.IsBlocked(e.participantKeyForHost(0)), "the executor is not struck for its voters")
	})
	t.Run("render_busy is no strike", func(t *testing.T) {
		limiter := NewParticipantRequestLimiter(2, 10)
		e := &Redundancy{devshardID: "1", participantLimiter: limiter, noRenderStrike: true}
		busy := make([]string, participantFailureStrikeThreshold)
		for i := range busy {
			busy[i] = "render_busy"
		}
		e.recordChallengeResult(rejects(busy...), nil)
		require.Zero(t, limiter.Snapshot([]string{e.participantKeyForHost(1)})[e.participantKeyForHost(1)].FailureStrikes)
	})
}

// The gateway labels the causes the voters' devshardd sends; a cause added
// there and not here would count as other.
func TestChallengeRejectCausesMatchTheVoters(t *testing.T) {
	for _, cause := range []string{
		host.ChallengeRejectNotLive, host.ChallengeRejectNotFinished, host.ChallengeRejectChallenged,
		host.ChallengeRejectExecutor, host.ChallengeRejectUnsupported, host.ChallengeRejectUnchecked,
		host.ChallengeRejectLate, host.ChallengeRejectNoRender, host.ChallengeRejectRenderBusy,
	} {
		require.Equal(t, cause, challengeRejectLabel(cause))
	}
	require.Len(t, challengeRejectCauses, 9)
}
