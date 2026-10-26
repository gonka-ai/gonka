package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/internal/testutil"
	"devshard/types"
	"devshard/user"
)

// emptyStrikeEnv is a one-host gateway whose participant strikes are read
// from the returned limiter.
func emptyStrikeEnv(t *testing.T) (*testProxyEnv, *ParticipantRequestLimiter) {
	t.Helper()
	env := setupTestProxyWithClients(t, []user.HostClient{streamContentThenStallClient{}})
	limiter := NewParticipantRequestLimiter(10, 10)
	env.proxy.redundancy.participantLimiter = limiter
	return env, limiter
}

// emptyAttempt is an attempt whose receipt and Finish reached the gateway with
// no content between them. finished applies the Finish on the session; bound
// makes the received stream hash to the Finish's served hash.
func emptyAttempt(t *testing.T, env *testProxyEnv, finished, bound bool) *inflight {
	t.Helper()
	prepared, err := env.session.PrepareInference(context.Background(), defaultParams())
	require.NoError(t, err)
	resp := applicableResponse(t, env, prepared.Nonce())
	if finished {
		require.NoError(t, env.session.ProcessResponse(prepared.HostIdx(), resp, prepared.Nonce()))
		require.True(t, env.session.IsNonceFinished(prepared.Nonce()))
	}
	if bound {
		resp.ReceivedResponseHashes = [][32]byte{[32]byte(testutil.TestServedHash)}
	}
	inf := &inflight{
		hostIdx:  prepared.HostIdx(),
		nonce:    prepared.Nonce(),
		resp:     resp,
		err:      errEmptyStream,
		sendTime: time.Now().Add(-time.Second),
	}
	inf.setReceiptAt(time.Now().Add(-900 * time.Millisecond))
	inf.outputChunks.Store(2)
	require.True(t, isEmptyStreamAttempt(inf))
	return inf
}

func emptyStreamStrikes(env *testProxyEnv, limiter *ParticipantRequestLimiter, inf *inflight) int {
	key := env.session.HostParticipantKey(inf.hostIdx)
	return limiter.Snapshot([]string{key})[key].FailureStrikes
}

func waitingEmptyStrikes(e *Redundancy) int {
	e.emptyStrikesMu.Lock()
	defer e.emptyStrikesMu.Unlock()
	return len(e.emptyStrikes)
}

// An empty stream that ran to its Finish strikes its host only once the
// challenge is over, and not at all when the voters found the stored body
// sound: a client's stop string, logit_bias on a lone byte, or </think> then
// EOS leave an honest stream with no text. Every other outcome keeps the
// strike the gateway applied before the challenge existed.
func TestEmptyStreamStrikeFollowsTheChallenge(t *testing.T) {
	vote := []*types.ChallengeVote{{Fault: types.ChallengeFault_CHALLENGE_FAULT_NONE}}
	for _, testCase := range []struct {
		outcome string
		result  user.ChallengeResult
		err     error
		strike  bool
	}{
		{outcome: "none", result: user.ChallengeResult{Votes: vote}},
		{outcome: "invalidated", result: user.ChallengeResult{Votes: vote, Faults: 2, Invalidated: true}, strike: true},
		{outcome: "below_threshold", result: user.ChallengeResult{Votes: vote, Faults: 1, Threshold: 1}, strike: true},
		{outcome: "incomplete", result: user.ChallengeResult{Votes: vote, UnansweredWeight: 2, Threshold: 1}, strike: true},
		{outcome: "no_votes", strike: true},
		{outcome: "failed", err: errors.New("collect challenge votes: context canceled"), strike: true},
		{outcome: "nonce_cap", result: user.ChallengeResult{NonceCap: true}, strike: true},
		{outcome: "replay_first", result: user.ChallengeResult{ReplayFirst: true}, strike: true},
	} {
		t.Run(testCase.outcome, func(t *testing.T) {
			env, limiter := emptyStrikeEnv(t)
			e := env.proxy.redundancy
			inf := emptyAttempt(t, env, true, true)

			e.recordStartedAttemptSamples([]*inflight{inf}, defaultParams(), true)
			require.Zero(t, emptyStreamStrikes(env, limiter, inf), "the strike waits for the challenge")

			result := testCase.result
			result.Nonce, result.Reason, result.ExecutorIdx = inf.nonce, types.ChallengeReason_CHALLENGE_REASON_EMPTY_STREAM, inf.hostIdx
			require.Equal(t, testCase.outcome, challengeOutcome(result, testCase.err))
			e.recordChallengeResult(result, testCase.err)
			want := 0
			if testCase.strike {
				want = 1
			}
			require.Equal(t, want, emptyStreamStrikes(env, limiter, inf))
			require.Zero(t, waitingEmptyStrikes(e))
		})
	}
}

// The challenge can end before the attempt is sampled: a race's attempts are
// sampled once the whole race is over, and an attempt's Finish can apply with
// another request's diff before that. The outcome then waits for the strike.
func TestEmptyStreamStrikeTakesAnOutcomeThatCameFirst(t *testing.T) {
	for _, testCase := range []struct {
		outcome string
		strike  bool
	}{
		{outcome: "none"},
		{outcome: "invalidated", strike: true},
	} {
		t.Run(testCase.outcome, func(t *testing.T) {
			env, limiter := emptyStrikeEnv(t)
			e := env.proxy.redundancy
			inf := emptyAttempt(t, env, true, true)

			e.settleEmptyStreamStrike(inf.nonce, types.ChallengeReason_CHALLENGE_REASON_EMPTY_STREAM, testCase.outcome)
			require.Equal(t, 1, waitingEmptyStrikes(e), "the outcome waits for its strike")
			e.recordStartedAttemptSamples([]*inflight{inf}, defaultParams(), true)

			want := 0
			if testCase.strike {
				want = 1
			}
			require.Equal(t, want, emptyStreamStrikes(env, limiter, inf))
			require.Zero(t, waitingEmptyStrikes(e))
		})
	}
}

// With no challenge to judge the stream, its host is struck at once, as
// before the challenge existed.
func TestEmptyStreamStrikesAtOnceWithoutAChallenge(t *testing.T) {
	t.Run("the stream does not hash to its Finish", func(t *testing.T) {
		// Cut on the way, or not the answer the executor signed for: the
		// voters judge the stored body, not this stream.
		env, limiter := emptyStrikeEnv(t)
		inf := emptyAttempt(t, env, true, false)
		env.proxy.redundancy.recordStartedAttemptSamples([]*inflight{inf}, defaultParams(), true)
		require.Equal(t, 1, emptyStreamStrikes(env, limiter, inf))
	})
	t.Run("no Finish applied", func(t *testing.T) {
		// The record times out instead; no challenge reaches it.
		env, limiter := emptyStrikeEnv(t)
		inf := emptyAttempt(t, env, false, true)
		env.proxy.redundancy.recordStartedAttemptSamples([]*inflight{inf}, defaultParams(), true)
		require.Equal(t, 1, emptyStreamStrikes(env, limiter, inf))
	})
	t.Run("a session before the challenge protocol", func(t *testing.T) {
		saved := types.EffectiveStateRootAndProtocolVersion
		t.Cleanup(func() { types.EffectiveStateRootAndProtocolVersion = saved })
		types.EffectiveStateRootAndProtocolVersion = "v6"
		env, limiter := emptyStrikeEnv(t)
		types.EffectiveStateRootAndProtocolVersion = saved
		require.Equal(t, "v6", env.session.StateMachine().ProtocolVersion())

		inf := emptyAttempt(t, env, true, true)
		env.proxy.redundancy.recordStartedAttemptSamples([]*inflight{inf}, defaultParams(), true)
		require.Equal(t, 1, emptyStreamStrikes(env, limiter, inf))
	})
	t.Run("a stopped gateway", func(t *testing.T) {
		env, limiter := emptyStrikeEnv(t)
		env.proxy.redundancy.Stop()
		inf := emptyAttempt(t, env, true, true)
		env.proxy.redundancy.recordStartedAttemptSamples([]*inflight{inf}, defaultParams(), true)
		require.Equal(t, 1, emptyStreamStrikes(env, limiter, inf))
	})
}

// A strike no outcome reaches lands when its wait runs out, and a late
// outcome does not strike again.
func TestEmptyStreamStrikeLandsWhenNoOutcomeComes(t *testing.T) {
	saved := emptyStreamVerdictWait
	emptyStreamVerdictWait = 10 * time.Millisecond
	t.Cleanup(func() { emptyStreamVerdictWait = saved })
	env, limiter := emptyStrikeEnv(t)
	e := env.proxy.redundancy
	inf := emptyAttempt(t, env, true, true)

	e.recordStartedAttemptSamples([]*inflight{inf}, defaultParams(), true)
	require.Eventually(t, func() bool { return emptyStreamStrikes(env, limiter, inf) == 1 }, 5*time.Second, 5*time.Millisecond)

	e.settleEmptyStreamStrike(inf.nonce, types.ChallengeReason_CHALLENGE_REASON_EMPTY_STREAM, "invalidated")
	require.Eventually(t, func() bool { return waitingEmptyStrikes(e) == 0 }, 5*time.Second, 5*time.Millisecond,
		"an outcome no strike claims is dropped when its wait runs out")
	require.Equal(t, 1, emptyStreamStrikes(env, limiter, inf))
}

// Stop applies the strikes still waiting: no challenge runs after it.
func TestStopAppliesTheWaitingEmptyStreamStrikes(t *testing.T) {
	env, limiter := emptyStrikeEnv(t)
	e := env.proxy.redundancy
	inf := emptyAttempt(t, env, true, true)

	e.recordStartedAttemptSamples([]*inflight{inf}, defaultParams(), true)
	require.Zero(t, emptyStreamStrikes(env, limiter, inf))
	e.Stop()
	require.Equal(t, 1, emptyStreamStrikes(env, limiter, inf))
	require.Zero(t, waitingEmptyStrikes(e))
}
