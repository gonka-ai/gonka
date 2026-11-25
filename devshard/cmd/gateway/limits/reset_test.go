package limits

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Test flow:
//  1. Narrow participant-a's windows with upstream faults, cut participant-b off with transport faults, and hold one lease on participant-a that costs two tokens each way.
//  2. Reset every pair.
//  3. Assert both windows are back at the initial size with the leased tokens still in flight, the peak equals them, the cut-off is closed with backoff 0, the counts are 2 pairs and 1 cut off, and the lift was narrated.
func TestResetAllReopensWindowsAndClosesCutoffsKeepingTheTokensInFlight(t *testing.T) {
	limiter := newTestLimiter(testConfig(), fixedNow(testEpoch))
	narrator := &recordingCutoffNarrator{}
	limiter.SetNarrator(narrator)
	release, admission := limiter.Acquire("participant-a", "model-a", TokenCost{Input: 2, Output: 2})
	require.Equal(t, AdmissionOpen, admission)
	limiter.answered("participant-a", "model-a", UpstreamFault)
	for range int(limiter.cfg.AfterFailures) {
		limiter.answered("participant-b", "model-a", TransportFault)
	}
	narrowedInput, _ := limiter.windowsOf(t, "participant-a", "model-a")
	require.Less(t, narrowedInput, 4.0, "the fault must have narrowed the window before the reset")
	require.Equal(t, AdmissionCutOff, limiter.Admits("participant-b", "model-a"))

	pairs, cutOff := limiter.ResetAll()

	require.Equal(t, 2, pairs)
	require.Equal(t, 1, cutOff)
	input, output := limiter.windowsOf(t, "participant-a", "model-a")
	require.Equal(t, 4.0, input)
	require.Equal(t, 4.0, output)
	state := limiter.states[key{participant: "participant-a", model: "model-a"}]
	require.Equal(t, int64(2), state.input.inflight)
	require.Equal(t, int64(2), state.input.peak)
	require.Equal(t, AdmissionOpen, limiter.Admits("participant-b", "model-a"))
	require.Zero(t, limiter.states[key{participant: "participant-b", model: "model-a"}].backoffCount)
	require.Equal(t, []recordedLift{{participant: "participant-b", model: "model-a", backoffCount: 1}}, narrator.lifts)

	release()
	require.Zero(t, state.input.inflight, "a lease taken before the reset gives back exactly its own tokens")
	require.Zero(t, state.output.inflight)
}

// Test flow:
//  1. Build a weighted limiter with hosts a and b on the earned model and cut host a off with transport faults.
//  2. Read the model's concurrency, then reset every pair.
//  3. Assert the cut-off host counted for nothing before and its 8 requests count again after, next to the 16 of host b.
func TestResetAllCountsAFormerlyCutOffPairInModelConcurrency(t *testing.T) {
	limiter := newTestLimiter(weightedConfig(), fixedNow(earnedEpoch))
	limiter.ObserveWeights(map[string]map[string]float64{earnedModel: {"a": 10_000, "b": 20_000}})
	limiter.admitOne("a", earnedModel)
	limiter.admitOne("b", earnedModel)
	for range int(limiter.cfg.AfterFailures) {
		limiter.answered("a", earnedModel, TransportFault)
	}
	require.Equal(t, int64(16), limiter.ModelConcurrency(earnedModel))

	limiter.ResetAll()

	require.Equal(t, int64(8+16), limiter.ModelConcurrency(earnedModel))
}

// Test flow:
//  1. Cut participant-a off, move the clock past the open period so the pair is half open, then reset.
//  2. Ask the limiter whether the pair admits and acquire a lease.
//  3. Assert the pair is closed again: the lease is granted and a following one is judged by the window, not by the half-open probe rule.
func TestResetAllClosesAHalfOpenCutoff(t *testing.T) {
	clock := newMovingClock(testEpoch)
	limiter := newTestLimiter(testConfig(), clock.now)
	for range int(limiter.cfg.AfterFailures) {
		limiter.answered("participant-a", "model-a", TransportFault)
	}
	clock.advance(time.Minute)
	require.Equal(t, CutoffHalfOpen, cutoffState(limiter.states[key{participant: "participant-a", model: "model-a"}], clock.now()))

	limiter.ResetAll()

	require.Equal(t, CutoffClosed, cutoffState(limiter.states[key{participant: "participant-a", model: "model-a"}], clock.now()))
	first := limiter.acquired(t, "participant-a", "model-a")
	second := limiter.acquired(t, "participant-a", "model-a")
	first()
	second()
}

// Test flow:
//  1. Launch an attempt on participant-a, advance the clock a minute and reset every pair.
//  2. Report the old attempt's upstream fault and AfterFailures transport faults, then the same faults from an attempt launched after the reset.
//  3. Assert the old attempt's results change nothing: the window stays at the initial size and the pair stays open; the new attempt's results narrow the window and cut the pair off.
func TestAResultOfAnAttemptLaunchedBeforeTheResetChangesNothing(t *testing.T) {
	clock := newMovingClock(testEpoch)
	limiter := newTestLimiter(testConfig(), clock.now)
	release, admitted := limiter.admitOne("participant-a", "model-a")
	require.True(t, admitted)
	release()
	launchedBefore := clock.now()
	clock.advance(time.Minute)
	limiter.ResetAll()
	launchedAfter := clock.now()
	report := func(verdict Verdict, launchedAt time.Time) {
		limiter.OnResult(Result{Participant: "participant-a", Model: "model-a", Verdict: verdict, Carried: oneToken, LaunchedAt: launchedAt})
	}

	report(UpstreamFault, launchedBefore)
	for range int(limiter.cfg.AfterFailures) {
		report(TransportFault, launchedBefore)
	}

	input, output := limiter.windowsOf(t, "participant-a", "model-a")
	require.Equal(t, 4.0, input)
	require.Equal(t, 4.0, output)
	require.Equal(t, AdmissionOpen, limiter.Admits("participant-a", "model-a"))

	report(UpstreamFault, launchedAfter)
	narrowedInput, _ := limiter.windowsOf(t, "participant-a", "model-a")
	require.Less(t, narrowedInput, 4.0)
	for range int(limiter.cfg.AfterFailures) {
		report(TransportFault, launchedAfter)
	}
	require.Equal(t, AdmissionCutOff, limiter.Admits("participant-a", "model-a"))
}

// Test flow:
//  1. Launch an attempt on participant-a, advance the clock a minute and reset every pair.
//  2. Count AfterFailures refusals of the old attempt on the idle host.
//  3. Assert the host stays open, then count the same refusals of an attempt launched after the reset and assert the host is cut off.
func TestARefusalOfAnAttemptLaunchedBeforeTheResetIsNotCounted(t *testing.T) {
	clock := newMovingClock(testEpoch)
	limiter := newTestLimiter(testConfig(), clock.now)
	launchedBefore := clock.now()
	clock.advance(time.Minute)
	limiter.ResetAll()
	launchedAfter := clock.now()

	for range int(limiter.cfg.AfterFailures) {
		limiter.CountRefusal("participant-a", "model-a", launchedBefore)
	}
	require.Equal(t, AdmissionOpen, limiter.Admits("participant-a", "model-a"))

	for range int(limiter.cfg.AfterFailures) {
		limiter.CountRefusal("participant-a", "model-a", launchedAfter)
	}
	require.Equal(t, AdmissionCutOff, limiter.Admits("participant-a", "model-a"))
}
