package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/host"
	"devshard/internal/testutil"
	"devshard/state"
	"devshard/types"
	"devshard/user"
)

// emptyStrikeEnv is a one-host gateway whose participant strikes are read
// from the returned limiter. Its host applies diffs, so a Finish applies.
func emptyStrikeEnv(t *testing.T) (*testProxyEnv, *ParticipantRequestLimiter) {
	t.Helper()
	return emptyStrikeEnvProtocol(t, types.DevshardStateRootAndProtocolVersion)
}

func emptyStrikeEnvProtocol(t *testing.T, protocol string) (*testProxyEnv, *ParticipantRequestLimiter) {
	t.Helper()
	env := setupTestProxyFloorClock(t, 1, nil, true, 1_000_000, 0, tipOracleChain(), false, 0,
		state.WithStateRootAndProtocolVersion(protocol))
	if types.ProtocolKnowsChallenge(protocol) {
		env.session.MarkHeightSeedGate(time.Now().Add(30 * time.Second))
		require.NoError(t, env.session.MaybeHeartbeat(context.Background()))
		require.Equal(t, user.HeightSeedStateOK, env.session.HeightSeedStatus().State)
		env.session.SetDisableHeightSyncHeartbeat(true)
	}
	limiter := NewParticipantRequestLimiter(10, 10)
	env.proxy.redundancy.participantLimiter = limiter
	return env, limiter
}

// emptyAttempt is an attempt whose receipt and Finish reached the gateway with
// no content between them. finished runs it on the host and applies its
// Finish on the session; otherwise the response is never processed, so the
// session holds no Finish. bound makes the received stream hash to the
// Finish's served hash.
func emptyAttempt(t *testing.T, env *testProxyEnv, finished, bound bool) *inflight {
	t.Helper()
	prepared, err := env.session.PrepareInference(context.Background(), defaultParams())
	require.NoError(t, err)
	servedHash := testutil.TestServedHash
	var resp *host.HostResponse
	if finished {
		resp, err = env.session.SendOnly(context.Background(), prepared, nil, nil)
		require.NoError(t, err)
		require.NoError(t, env.session.ProcessResponse(prepared.HostIdx(), resp, prepared.Nonce()))
		require.NoError(t, env.session.SendPendingDiff(context.Background()))
		rec, ok := env.session.StateMachine().Inference(prepared.Nonce())
		require.True(t, ok)
		require.Equal(t, types.StatusFinished, rec.Status)
		servedHash = rec.ServedHash
	} else {
		resp = applicableResponse(t, env, prepared.Nonce())
	}
	resp.ReceivedResponseHashes = nil
	if bound {
		resp.ReceivedResponseHashes = [][32]byte{[32]byte(servedHash)}
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
	env.proxy.redundancy.trackEmptyStreamAttempt(inf)
	return inf
}

// judgedResult is a challenge result for inf's nonce that names the Finish
// the session applied, as RunChallenge does once it asks the hosts.
func judgedResult(t *testing.T, env *testProxyEnv, inf *inflight, result user.ChallengeResult) user.ChallengeResult {
	t.Helper()
	rec, ok := env.session.StateMachine().Inference(inf.nonce)
	require.True(t, ok)
	result.Nonce, result.Reason, result.ExecutorIdx = inf.nonce, types.ChallengeReason_CHALLENGE_REASON_EMPTY_STREAM, inf.hostIdx
	result.ResponseHash, result.ServedHash = rec.ResponseHash, rec.ServedHash
	return result
}

func emptyStreamStrikes(env *testProxyEnv, limiter *ParticipantRequestLimiter, inf *inflight) int {
	key := env.session.HostParticipantKey(inf.hostIdx)
	return limiter.Snapshot([]string{key})[key].FailureStrikes
}

func waitingEmptyStrikes(e *Redundancy) int {
	e.emptyStrikesMu.Lock()
	defer e.emptyStrikesMu.Unlock()
	waiting := len(e.emptyStrikes)
	for _, early := range e.emptyStrikeAttempts {
		if early != nil {
			waiting++
		}
	}
	return waiting
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

			result := judgedResult(t, env, inf, testCase.result)
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

			e.settleEmptyStreamStrike(judgedResult(t, env, inf, user.ChallengeResult{}), testCase.outcome)
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
		env, limiter := emptyStrikeEnvProtocol(t, "v6")
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
	env, limiter := emptyStrikeEnv(t)
	e := env.proxy.redundancy
	inf := emptyAttempt(t, env, true, true)

	e.recordStartedAttemptSamples([]*inflight{inf}, defaultParams(), true)
	strike := e.emptyStrikes[inf.nonce]
	require.NotNil(t, strike)
	strike.timer.Stop()
	e.expireEmptyStreamStrike(inf.nonce, strike)
	require.Equal(t, 1, emptyStreamStrikes(env, limiter, inf))

	e.settleEmptyStreamStrike(judgedResult(t, env, inf, user.ChallengeResult{}), "invalidated")
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

// A host's response can list a second, conflicting Finish for the nonce that
// the session drops. A stream that hashes to the dropped Finish is struck at
// once: the voters judge the stored body against the Finish that applied.
func TestEmptyStreamStrikeIgnoresAFinishTheSessionDropped(t *testing.T) {
	env, limiter := emptyStrikeEnv(t)
	prepared, err := env.session.PrepareInference(context.Background(), defaultParams())
	require.NoError(t, err)
	resp, err := env.session.SendOnly(context.Background(), prepared, nil, nil)
	require.NoError(t, err)
	droppedResponse, droppedServed := sha256.Sum256([]byte("dropped response")), sha256.Sum256([]byte("dropped served"))
	idx := prepared.HostIdx()
	dropped := &types.MsgFinishInference{
		InferenceId:  prepared.Nonce(),
		EscrowId:     "escrow-proxy",
		ExecutorSlot: env.group[idx].SlotID,
		ResponseHash: droppedResponse[:],
		ServedHash:   droppedServed[:],
	}
	dropped.ProposerSig = testutil.SignProposerTx(t, env.hostSigners[idx], dropped)
	resp.Mempool = append(resp.Mempool, &types.DevshardTx{Tx: &types.DevshardTx_FinishInference{FinishInference: dropped}})
	require.NoError(t, env.session.ProcessResponse(idx, resp, prepared.Nonce()))
	require.NoError(t, env.session.SendPendingDiff(context.Background()))
	rec, ok := env.session.StateMachine().Inference(prepared.Nonce())
	require.True(t, ok)
	require.Equal(t, types.StatusFinished, rec.Status)
	require.NotEqual(t, droppedServed[:], rec.ServedHash, "the first Finish applied")

	resp.ReceivedResponseHashes = [][32]byte{droppedServed}
	inf := &inflight{hostIdx: idx, nonce: prepared.Nonce(), resp: resp, err: errEmptyStream, sendTime: time.Now().Add(-time.Second)}
	inf.setReceiptAt(time.Now().Add(-900 * time.Millisecond))
	inf.outputChunks.Store(2)
	require.True(t, isEmptyStreamAttempt(inf))

	e := env.proxy.redundancy
	e.trackEmptyStreamAttempt(inf)
	e.settleEmptyStreamStrike(user.ChallengeResult{
		Nonce: inf.nonce, Reason: types.ChallengeReason_CHALLENGE_REASON_STREAM_UNBOUND,
		ResponseHash: droppedResponse[:], ServedHash: droppedServed[:],
	}, "none")
	// Even a NONE that matches the bytes received cannot bless the Finish
	// that this session dropped in favor of a different committed answer.
	e.recordStartedAttemptSamples([]*inflight{inf}, defaultParams(), true)
	require.Equal(t, 1, emptyStreamStrikes(env, limiter, inf))
	require.Zero(t, waitingEmptyStrikes(env.proxy.redundancy))
}

// A queued Finish can be replaced before it applies. A NONE verdict on a
// Finish the stream does not hash to is about another answer, so it does not
// clear the strike, whichever of the two came first.
func TestEmptyStreamStrikeNeedsTheVerdictOnItsOwnFinish(t *testing.T) {
	other := sha256.Sum256([]byte("another answer"))
	vote := []*types.ChallengeVote{{Fault: types.ChallengeFault_CHALLENGE_FAULT_NONE}}
	for _, outcomeFirst := range []bool{false, true} {
		env, limiter := emptyStrikeEnv(t)
		e := env.proxy.redundancy
		inf := emptyAttempt(t, env, true, true)
		result := judgedResult(t, env, inf, user.ChallengeResult{Votes: vote})
		result.ResponseHash, result.ServedHash = other[:], other[:]
		require.Equal(t, "none", challengeOutcome(result, nil))

		if outcomeFirst {
			e.recordChallengeResult(result, nil)
			e.recordStartedAttemptSamples([]*inflight{inf}, defaultParams(), true)
		} else {
			e.recordStartedAttemptSamples([]*inflight{inf}, defaultParams(), true)
			require.Zero(t, emptyStreamStrikes(env, limiter, inf), "the strike waits for the challenge")
			e.recordChallengeResult(result, nil)
		}
		require.Equal(t, 1, emptyStreamStrikes(env, limiter, inf), "outcomeFirst=%v", outcomeFirst)
		require.Zero(t, waitingEmptyStrikes(e))
	}
}

// STREAM_UNBOUND can start while the response is open, before the gateway
// learns it contained no content. A Finish applied by another request's diff
// can therefore be judged before this race is sampled.
func TestEmptyStreamStrikeHandlesStreamUnboundTiming(t *testing.T) {
	for _, outcomeFirst := range []bool{true, false} {
		for _, tc := range []struct {
			name            string
			outcome         string
			differentFinish bool
			missingHashes   bool
			want            int
		}{
			{name: "same Finish", outcome: "none"},
			{name: "different Finish", outcome: "none", differentFinish: true, want: 1},
			{name: "missing Finish", outcome: "none", missingHashes: true, want: 1},
			{name: "invalidated", outcome: "invalidated", want: 1},
			{name: "fault below threshold", outcome: "below_threshold", want: 1},
			{name: "incomplete", outcome: "incomplete", want: 1},
			{name: "no votes", outcome: "no_votes", want: 1},
			{name: "failed", outcome: "failed", want: 1},
		} {
			t.Run(fmt.Sprintf("%s/early=%v", tc.name, outcomeFirst), func(t *testing.T) {
				env, limiter := emptyStrikeEnv(t)
				e := env.proxy.redundancy
				inf := emptyAttempt(t, env, true, true)
				result := judgedResult(t, env, inf, user.ChallengeResult{})
				result.Reason = types.ChallengeReason_CHALLENGE_REASON_STREAM_UNBOUND
				if tc.differentFinish {
					other := sha256.Sum256([]byte("replaced Finish"))
					result.ResponseHash, result.ServedHash = other[:], other[:]
				}
				if tc.missingHashes {
					result.ResponseHash, result.ServedHash = nil, nil
				}
				if outcomeFirst {
					e.settleEmptyStreamStrike(result, tc.outcome)
					require.Equal(t, 1, waitingEmptyStrikes(e), "early STREAM_UNBOUND must survive until sampling")
					e.recordStartedAttemptSamples([]*inflight{inf}, defaultParams(), true)
				} else {
					e.recordStartedAttemptSamples([]*inflight{inf}, defaultParams(), true)
					e.settleEmptyStreamStrike(result, tc.outcome)
				}
				require.Equal(t, tc.want, emptyStreamStrikes(env, limiter, inf))
				require.Empty(t, e.emptyStrikeAttempts)
				require.Zero(t, waitingEmptyStrikes(e))
				// A duplicate late callback cannot recreate a timer or change a
				// settled penalty, including a NONE on a different Finish.
				e.settleEmptyStreamStrike(result, "none")
				require.Equal(t, tc.want, emptyStreamStrikes(env, limiter, inf))
				require.Zero(t, waitingEmptyStrikes(e))
			})
		}
	}
}

func TestEmptyStreamStrikeEarlyCacheFollowsActiveAttempts(t *testing.T) {
	env, _ := emptyStrikeEnv(t)
	e := env.proxy.redundancy
	result := user.ChallengeResult{Reason: types.ChallengeReason_CHALLENGE_REASON_STREAM_UNBOUND}
	for nonce := uint64(1); nonce <= 10000; nonce++ {
		result.Nonce = nonce
		e.settleEmptyStreamStrike(result, "none")
	}
	require.Empty(t, e.emptyStrikeAttempts, "untracked stream verdicts must not accumulate")
	require.Empty(t, e.emptyStrikes, "early verdicts never create expiry timers")

	// No fixed-size eviction is safe: every concurrently active attempt must
	// keep its verdict until sampled, however many other verdicts arrive.
	const active = 2048
	attempts := make([]*inflight, active)
	hash := sha256.Sum256([]byte("Finish"))
	result.ResponseHash, result.ServedHash = hash[:], hash[:]
	result.Votes = make([]*types.ChallengeVote, 1000)
	for i := range attempts {
		attempts[i] = &inflight{nonce: uint64(i + 1)}
		e.trackEmptyStreamAttempt(attempts[i])
		result.Nonce = attempts[i].nonce
		e.settleEmptyStreamStrike(result, "none")
	}
	require.Len(t, e.emptyStrikeAttempts, active)
	require.Empty(t, e.emptyStrikes)
	first := e.emptyStrikeAttempts[1]
	require.Equal(t, hash[:], first.result.ResponseHash)
	require.Equal(t, hash[:], first.result.ServedHash)
	require.Empty(t, first.result.Votes, "do not retain signed vote payloads")
	hash[0] ^= 0xff
	require.NotEqual(t, hash[:], first.result.ResponseHash, "cached hashes must own their storage")
	result.Nonce = 1
	e.settleEmptyStreamStrike(result, "invalidated")
	require.Same(t, first, e.emptyStrikeAttempts[1], "first terminal callback owns the outcome")

	e.forgetEmptyStreamAttempts(attempts)
	require.Empty(t, e.emptyStrikeAttempts)
	for nonce := uint64(1); nonce <= active; nonce++ {
		result.Nonce = nonce
		e.settleEmptyStreamStrike(result, "none")
	}
	require.Empty(t, e.emptyStrikeAttempts, "late callbacks cannot reopen completed attempts")
}

func TestEmptyStreamStrikeSamplingDiscardsIrrelevantEarlyVerdicts(t *testing.T) {
	for _, endedByGateway := range []bool{false, true} {
		t.Run(fmt.Sprintf("cancelled=%v", endedByGateway), func(t *testing.T) {
			env, limiter := emptyStrikeEnv(t)
			e := env.proxy.redundancy
			inf := emptyAttempt(t, env, true, true)
			e.settleEmptyStreamStrike(judgedResult(t, env, inf, user.ChallengeResult{}), "none")
			if endedByGateway {
				inf.endedByGateway.Store(true)
			} else {
				// A successful nonempty stream never needs an empty penalty.
				inf.err = nil
				inf.outputChunks.Store(3)
				inf.contentChunks.Store(1)
			}
			e.recordSampleOnce(inf, defaultParams(), true)
			require.Empty(t, e.emptyStrikeAttempts)
			require.Zero(t, waitingEmptyStrikes(e))
			require.Zero(t, emptyStreamStrikes(env, limiter, inf))
		})
	}
}

func TestEmptyStreamStrikeConcurrentSamplingAndVerdict(t *testing.T) {
	env, limiter := emptyStrikeEnv(t)
	e := env.proxy.redundancy
	for i := 0; i < 30; i++ {
		inf := emptyAttempt(t, env, true, true)
		result := judgedResult(t, env, inf, user.ChallengeResult{})
		result.Reason = types.ChallengeReason_CHALLENGE_REASON_STREAM_UNBOUND
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); e.recordSampleOnce(inf, defaultParams(), true) }()
		go func() { defer wg.Done(); e.settleEmptyStreamStrike(result, "none") }()
		wg.Wait()
		require.Zero(t, emptyStreamStrikes(env, limiter, inf))
		require.Zero(t, waitingEmptyStrikes(e))
		require.Empty(t, e.emptyStrikeAttempts)
	}
}

func TestEmptyStreamStrikeStopRacesExpiryAndVerdict(t *testing.T) {
	for i := 0; i < 20; i++ {
		env, limiter := emptyStrikeEnv(t)
		e := env.proxy.redundancy
		inf := emptyAttempt(t, env, true, true)
		e.recordSampleOnce(inf, defaultParams(), true)
		strike := e.emptyStrikes[inf.nonce]
		require.NotNil(t, strike)
		strike.timer.Stop() // invoke expiry synchronously below
		result := judgedResult(t, env, inf, user.ChallengeResult{})
		var wg sync.WaitGroup
		wg.Add(3)
		go func() { defer wg.Done(); e.Stop() }()
		go func() { defer wg.Done(); e.expireEmptyStreamStrike(inf.nonce, strike) }()
		go func() { defer wg.Done(); e.settleEmptyStreamStrike(result, "invalidated") }()
		wg.Wait()
		require.Equal(t, 1, emptyStreamStrikes(env, limiter, inf), "only one terminal path may apply the strike")
		require.Zero(t, waitingEmptyStrikes(e))
		e.trackEmptyStreamAttempt(&inflight{nonce: inf.nonce + 1})
		require.Empty(t, e.emptyStrikeAttempts, "Stop forbids new early verdict registrations")
	}
}

// Some races never sample an attempt at all: phase changes abort it, or a
// transport ignores cancellation. Their finalizer must still discard early
// verdicts, without depending on an hour-long timer.
func TestEmptyStreamStrikeRaceCleanupDiscardsUnsampledAttempts(t *testing.T) {
	for _, stillSending := range []bool{false, true} {
		t.Run(fmt.Sprintf("stillSending=%v", stillSending), func(t *testing.T) {
			env, limiter := emptyStrikeEnv(t)
			e := env.proxy.redundancy
			inf := emptyAttempt(t, env, true, true)
			result := judgedResult(t, env, inf, user.ChallengeResult{})
			e.settleEmptyStreamStrike(result, "none")
			inf.processOnce.Do(func() {}) // helper already applied the response
			if stillSending {
				inf.done = make(chan struct{})
			} else {
				inf.phaseTransitionAborted = true
			}
			_ = e.finishRaceOutcome(context.Background(), []*inflight{inf}, defaultParams(), Decision{}, inf.nonce, raceFinishOptions{})
			require.Empty(t, e.emptyStrikeAttempts)
			require.Zero(t, waitingEmptyStrikes(e))
			require.Zero(t, emptyStreamStrikes(env, limiter, inf))
			e.settleEmptyStreamStrike(result, "none")
			require.Zero(t, waitingEmptyStrikes(e))
		})
	}
}

func TestEmptyStreamStrikeStopDiscardsOnlyEarlyVerdicts(t *testing.T) {
	env, limiter := emptyStrikeEnv(t)
	e := env.proxy.redundancy
	inf := emptyAttempt(t, env, true, true)
	e.settleEmptyStreamStrike(judgedResult(t, env, inf, user.ChallengeResult{}), "invalidated")
	e.Stop()
	require.Empty(t, e.emptyStrikeAttempts)
	require.Zero(t, waitingEmptyStrikes(e))
	require.Zero(t, emptyStreamStrikes(env, limiter, inf), "an unsampled verdict alone is not an empty-stream penalty")
}

// A valid NONE verdict received before sampling must not turn into an
// expiry penalty later. This is the original timing regression.
func TestEmptyStreamStrikeEarlyStreamUnboundDoesNotExpire(t *testing.T) {
	env, limiter := emptyStrikeEnv(t)
	e := env.proxy.redundancy
	inf := emptyAttempt(t, env, true, true)
	result := judgedResult(t, env, inf, user.ChallengeResult{})
	result.Reason = types.ChallengeReason_CHALLENGE_REASON_STREAM_UNBOUND
	e.settleEmptyStreamStrike(result, "none")
	e.recordSampleOnce(inf, defaultParams(), true)
	if strike := e.emptyStrikes[inf.nonce]; strike != nil {
		strike.timer.Stop()
		e.expireEmptyStreamStrike(inf.nonce, strike)
	}
	require.Zero(t, emptyStreamStrikes(env, limiter, inf), "a completed honest challenge must not expire into a strike")
	require.Zero(t, waitingEmptyStrikes(e))
}

// Stop marks the gateway stopped before it joins race finalizers. A verdict
// already known to a still-active attempt must remain usable during that join,
// including a callback that arrives after Stop starts waiting.
func TestEmptyStreamStrikeStopHonorsEarlyVerdictWhileJoining(t *testing.T) {
	for _, duringStop := range []bool{false, true} {
		for _, sameFinish := range []bool{false, true} {
			t.Run(fmt.Sprintf("duringStop=%v/sameFinish=%v", duringStop, sameFinish), func(t *testing.T) {
				env, limiter := emptyStrikeEnv(t)
				e := env.proxy.redundancy
				inf := emptyAttempt(t, env, true, true)
				result := judgedResult(t, env, inf, user.ChallengeResult{})
				result.Reason = types.ChallengeReason_CHALLENGE_REASON_STREAM_UNBOUND
				if !sameFinish {
					other := sha256.Sum256([]byte("another Finish during Stop"))
					result.ResponseHash, result.ServedHash = other[:], other[:]
				}
				if !duringStop {
					e.settleEmptyStreamStrike(result, "none")
				}
				ready, sample := make(chan struct{}), make(chan struct{})
				e.goTrackedRaceCleanup(context.Background(), func(context.Context) {
					close(ready)
					<-sample
					e.recordSampleOnce(inf, defaultParams(), true)
				})
				<-ready
				stopped := make(chan struct{})
				go func() { e.Stop(); close(stopped) }()
				require.Eventually(t, e.stopped.Load, time.Second, time.Millisecond)
				if duringStop {
					e.settleEmptyStreamStrike(result, "none")
				}
				close(sample)
				<-stopped
				want := 1
				if sameFinish {
					want = 0
				}
				require.Equal(t, want, emptyStreamStrikes(env, limiter, inf))
				require.Empty(t, e.emptyStrikeAttempts)
				require.Zero(t, waitingEmptyStrikes(e))
			})
		}
	}
}

func TestEmptyStreamStrikeDelayCoversCollection(t *testing.T) {
	now := time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name     string
		deadline time.Time
		want     time.Duration
	}{
		{name: "default 64 minute collection", deadline: now.Add(64 * time.Minute), want: 124 * time.Minute},
		{name: "short configured window", deadline: now.Add(3*time.Minute + 17*time.Second), want: time.Hour + 3*time.Minute + 17*time.Second},
		{name: "partly elapsed window", deadline: now.Add(34 * time.Minute), want: 94 * time.Minute},
		{name: "expired window", deadline: now.Add(-3 * time.Hour), want: time.Hour},
		{name: "exact collection deadline", deadline: now, want: time.Hour},
		{name: "missing deadline", want: time.Hour},
		{name: "local clock behind receipt", deadline: now.Add(69 * time.Minute), want: 129 * time.Minute},
		{name: "far future receipt saturates", deadline: time.UnixMilli(math.MaxInt64), want: time.Duration(math.MaxInt64)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, emptyStreamStrikeDelay(now, tc.deadline))
		})
	}
}

// A received receipt/Finish can be queued until the next diff. The state
// record still has no confirmation clock, but its verified queued confirm
// must determine the same deadline the eventual RunChallenge uses.
func TestEmptyStreamStrikeDeadlineUsesQueuedConfirmation(t *testing.T) {
	for _, forgedTime := range []bool{false, true} {
		t.Run(fmt.Sprintf("forgedTime=%v", forgedTime), func(t *testing.T) {
			env := setupTestProxyFloorClock(t, 1, nil, true, 1_000_000, 0, tipOracleChain(), false, 32*60,
				state.WithStateRootAndProtocolVersion(types.DevshardStateRootAndProtocolVersion))
			env.session.MarkHeightSeedGate(time.Now().Add(30 * time.Second))
			require.NoError(t, env.session.MaybeHeartbeat(context.Background()))
			require.Equal(t, user.HeightSeedStateOK, env.session.HeightSeedStatus().State)
			env.session.SetDisableHeightSyncHeartbeat(true)
			prepared, err := env.session.PrepareInference(context.Background(), defaultParams())
			require.NoError(t, err)
			resp, err := env.session.SendOnly(context.Background(), prepared, nil, nil)
			require.NoError(t, err)
			require.Positive(t, resp.ObservedTimeMs)
			honestTime := resp.ObservedTimeMs
			var finish *types.MsgFinishInference
			var onlyFinish []*types.DevshardTx
			for _, tx := range resp.Mempool {
				if fi := tx.GetFinishInference(); fi != nil && fi.InferenceId == prepared.Nonce() {
					finish = fi
					onlyFinish = append(onlyFinish, tx)
				}
			}
			require.NotNil(t, finish)
			if forgedTime {
				// The streaming receipt callback already queued the authentic
				// confirmation. A forged final envelope cannot move its clock;
				// omit the mempool copy to isolate that saved receipt.
				resp.Mempool = onlyFinish
				resp.ObservedTimeMs += 5000
			}
			require.NoError(t, env.session.ProcessResponse(prepared.HostIdx(), resp, prepared.Nonce()))
			rec, ok := env.session.StateMachine().Inference(prepared.Nonce())
			require.True(t, ok)
			require.Equal(t, types.StatusPending, rec.Status)
			require.Zero(t, rec.ConfirmedAtTimeMs)
			now := time.Now()
			received := [][32]byte{[32]byte(finish.ServedHash)}
			deadline, bound := env.session.ReceivedStreamChallengeDeadline(prepared.Nonce(), received, now)
			require.True(t, bound)
			fetchEnd, armed := types.ChallengeFetchDeadline(honestTime, env.session.StateMachine().Config())
			require.True(t, armed)
			require.Equal(t, fetchEnd.Add(user.ChallengeVoteSlack), deadline)
			require.Greater(t, deadline.Sub(now), time.Hour, "a fixed one-hour strike timer would cut collection short")
			require.NoError(t, env.session.SendPendingDiff(context.Background()))
			applied, ok := env.session.StateMachine().Inference(prepared.Nonce())
			require.True(t, ok)
			require.Equal(t, types.StatusFinished, applied.Status)
			require.Equal(t, honestTime, applied.ConfirmedAtTimeMs)
			after, bound := env.session.ReceivedStreamChallengeDeadline(prepared.Nonce(), received, now.Add(time.Minute))
			require.True(t, bound)
			require.Equal(t, deadline, after, "application and delivery time must not move collection's deadline")
			_, bound = env.session.ReceivedStreamChallengeDeadline(prepared.Nonce(), nil, now)
			require.False(t, bound)
			_, bound = env.session.ReceivedStreamChallengeDeadline(prepared.Nonce()+999, received, now)
			require.False(t, bound)
		})
	}
}
