package limits

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const (
	probeTestParticipant = "participant-a"
	probeTestModel       = "model-a"
	olderLeaseCount      = 3
)

var launchedBeforeCutoff = testEpoch.Add(-time.Second)

type heldLeases []func()

func (held heldLeases) releaseAll() {
	for _, release := range held {
		release()
	}
}

func (l *ParticipantLimiter) resultLaunchedAt(verdict Verdict, launchedAt time.Time) {
	l.OnResult(Result{Participant: probeTestParticipant, Model: probeTestModel, Verdict: verdict, Carried: oneToken, LaunchedAt: launchedAt})
}

func cutOffWithOlderLeases(t *testing.T) (*ParticipantLimiter, *movingClock, *recordingCutoffNarrator, heldLeases) {
	t.Helper()
	clock := newMovingClock(testEpoch)
	limiter := newTestLimiter(testConfig(), clock.now)
	narrator := &recordingCutoffNarrator{}
	limiter.SetNarrator(narrator)
	held := make(heldLeases, 0, olderLeaseCount)
	for range olderLeaseCount {
		held = append(held, limiter.acquired(t, probeTestParticipant, probeTestModel))
	}
	for range int(limiter.cfg.AfterFailures) {
		limiter.answered(probeTestParticipant, probeTestModel, TransportFault)
	}
	require.Len(t, narrator.cutoffs, 1)
	return limiter, clock, narrator, held
}

// Test flow:
//  1. Hold three leases on a pair, trip its cut-off with transport faults, and keep the leases in flight through the whole test.
//  2. Assert the pair refuses while the cut-off is open, then admits once the open period has passed.
//  3. Take the probe and assert a second request is refused, also after the older leases release.
//  4. Release the probe without a verdict and assert the pair admits a replacement probe.
func TestAHalfOpenPairGetsItsProbeWhileOlderLeasesAreStillInFlight(t *testing.T) {
	limiter, clock, _, older := cutOffWithOlderLeases(t)

	clock.advance(limiter.cfg.BaseOpen / 2)
	require.Equal(t, AdmissionCutOff, limiter.Admits(probeTestParticipant, probeTestModel))
	clock.advance(limiter.cfg.BaseOpen)
	require.Equal(t, AdmissionOpen, limiter.Admits(probeTestParticipant, probeTestModel), "the probe must not wait for leases taken before the cut-off")

	probe := limiter.acquired(t, probeTestParticipant, probeTestModel)
	_, refused := limiter.Acquire(probeTestParticipant, probeTestModel, oneToken)
	require.Equal(t, AdmissionCutOff, refused, "one probe at a time")
	older.releaseAll()
	_, refused = limiter.Acquire(probeTestParticipant, probeTestModel, oneToken)
	require.Equal(t, AdmissionCutOff, refused, "older leases ending must not open a second probe")
	probe()
	require.Equal(t, AdmissionOpen, limiter.Admits(probeTestParticipant, probeTestModel), "a probe that ended without a verdict is replaced")
}

// Test flow:
//  1. Trip a cut-off with three leases in flight, let it lapse and take the probe.
//  2. Answer a fault and a success of attempts launched before the cut-off.
//  3. Assert the cut-off was neither reopened nor closed by them.
//  4. Answer a fault of an attempt launched after the cut-off and assert it reopens as a failed probe.
func TestResultsOfAttemptsLaunchedBeforeTheCutoffLeaveItAlone(t *testing.T) {
	limiter, clock, narrator, older := cutOffWithOlderLeases(t)
	clock.advance(2 * limiter.cfg.BaseOpen)
	limiter.acquired(t, probeTestParticipant, probeTestModel)

	limiter.resultLaunchedAt(TransportFault, launchedBeforeCutoff)
	limiter.resultLaunchedAt(Success, launchedBeforeCutoff)

	require.Len(t, narrator.cutoffs, 1, "a fault of an older attempt is not a failed probe")
	require.Empty(t, narrator.lifts, "a success of an older attempt does not prove the host is back")
	require.Equal(t, CutoffHalfOpen, cutoffState(limiter.states[key{participant: probeTestParticipant, model: probeTestModel}], clock.now()))

	limiter.resultLaunchedAt(TransportFault, clock.now())

	require.Len(t, narrator.cutoffs, 2)
	require.Equal(t, cutoffReasonProbeFailed, narrator.cutoffs[1].reason)
	older.releaseAll()
}

// Test flow:
//  1. Trip a cut-off, let it lapse without a probe, then answer three times the trip threshold of faults of attempts launched before the cut-off.
//  2. Assert only the first cut-off was narrated.
func TestFaultsOfOlderAttemptsAfterTheOpenPeriodDoNotReopenIt(t *testing.T) {
	limiter, clock, narrator, _ := cutOffWithOlderLeases(t)
	clock.advance(2 * limiter.cfg.BaseOpen)

	for range 3 * int(limiter.cfg.AfterFailures) {
		limiter.resultLaunchedAt(TransportFault, launchedBeforeCutoff)
	}

	require.Len(t, narrator.cutoffs, 1)
}

// Test flow:
//  1. Trip a cut-off with three leases in flight, let it lapse and take the probe.
//  2. Answer a success of an attempt launched after the cut-off.
//  3. Assert the cut-off closes, the backoff count decays and the pair admits again with the older leases still in flight.
func TestAProbeThatSucceedsClosesTheCutoffWithOlderLeasesOut(t *testing.T) {
	limiter, clock, narrator, _ := cutOffWithOlderLeases(t)
	clock.advance(2 * limiter.cfg.BaseOpen)
	probe := limiter.acquired(t, probeTestParticipant, probeTestModel)

	limiter.resultLaunchedAt(Success, clock.now())
	probe()

	require.Equal(t, []recordedLift{{participant: probeTestParticipant, model: probeTestModel, backoffCount: 0}}, narrator.lifts)
	require.Equal(t, CutoffClosed, cutoffState(limiter.states[key{participant: probeTestParticipant, model: probeTestModel}], clock.now()))
	require.Equal(t, AdmissionOpen, limiter.Admits(probeTestParticipant, probeTestModel))
}

// Test flow:
//  1. Trip a cut-off with three leases in flight, let it lapse and take the probe.
//  2. Answer a transport fault of an attempt launched after the cut-off.
//  3. Assert the cut-off reopens as a failed probe with the next backoff step and refuses a request.
func TestAProbeThatFailsReopensTheCutoffWithTheNextBackoff(t *testing.T) {
	limiter, clock, narrator, _ := cutOffWithOlderLeases(t)
	clock.advance(2 * limiter.cfg.BaseOpen)
	limiter.acquired(t, probeTestParticipant, probeTestModel)

	limiter.resultLaunchedAt(TransportFault, clock.now())

	require.Len(t, narrator.cutoffs, 2)
	require.Equal(t, cutoffReasonProbeFailed, narrator.cutoffs[1].reason)
	require.Equal(t, 2, narrator.cutoffs[1].backoffCount)
	require.Equal(t, time.Duration(float64(limiter.cfg.BaseOpen)*cutoffBackoffMultiplier), narrator.cutoffs[1].cutOffFor)
	require.Equal(t, AdmissionCutOff, limiter.Admits(probeTestParticipant, probeTestModel))
}

// Test flow:
//  1. Trip a cut-off with three leases in flight, let it lapse, take the probe and release it.
//  2. Report a refusal of an attempt launched before the cut-off and assert the cut-off is not reopened.
//  3. Report a refusal of an attempt launched after it and assert the cut-off reopens as a failed probe although the older leases are out.
func TestARefusedProbeReopensTheCutoffWhileOlderLeasesAreOut(t *testing.T) {
	limiter, clock, narrator, older := cutOffWithOlderLeases(t)
	clock.advance(2 * limiter.cfg.BaseOpen)
	limiter.acquired(t, probeTestParticipant, probeTestModel)()

	limiter.CountRefusal(probeTestParticipant, probeTestModel, launchedBeforeCutoff)
	require.Len(t, narrator.cutoffs, 1, "a refusal of an older attempt is not the probe's")

	limiter.CountRefusal(probeTestParticipant, probeTestModel, clock.now())

	require.Len(t, narrator.cutoffs, 2)
	require.Equal(t, cutoffReasonProbeFailed, narrator.cutoffs[1].reason)
	older.releaseAll()
}

// Test flow:
//  1. Trip a cut-off with three leases in flight, let it lapse, take the probe, then release the probe and the older leases so the host is idle.
//  2. Report a refusal of an attempt launched before the cut-off.
//  3. Assert the cut-off is not reopened, since only the probe decides.
func TestARefusalOfAnOlderAttemptFromAnIdleHostDoesNotReopenTheCutoff(t *testing.T) {
	limiter, clock, narrator, older := cutOffWithOlderLeases(t)
	clock.advance(2 * limiter.cfg.BaseOpen)
	limiter.acquired(t, probeTestParticipant, probeTestModel)()
	older.releaseAll()

	limiter.CountRefusal(probeTestParticipant, probeTestModel, launchedBeforeCutoff)

	require.Len(t, narrator.cutoffs, 1)
}

// Test flow:
//  1. Take a probe, fail it so the cut-off reopens, let the new cut-off lapse and take the new probe.
//  2. Release the first probe's lease, taken under the earlier cut-off.
//  3. Assert the new probe still counts, so a second request is refused until it ends.
func TestAnEarlierProbesReleaseDoesNotFreeTheNextProbe(t *testing.T) {
	limiter, clock, narrator, _ := cutOffWithOlderLeases(t)
	clock.advance(2 * limiter.cfg.BaseOpen)
	firstProbe := limiter.acquired(t, probeTestParticipant, probeTestModel)
	limiter.resultLaunchedAt(TransportFault, clock.now())
	require.Len(t, narrator.cutoffs, 2)
	clock.advance(2 * narrator.cutoffs[1].cutOffFor)
	secondProbe := limiter.acquired(t, probeTestParticipant, probeTestModel)

	firstProbe()

	_, refused := limiter.Acquire(probeTestParticipant, probeTestModel, oneToken)
	require.Equal(t, AdmissionCutOff, refused)
	secondProbe()
}
