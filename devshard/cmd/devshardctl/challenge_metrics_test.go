package main

import (
	"errors"
	"testing"

	promtestutil "github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

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
