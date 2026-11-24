package perf

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const (
	rungUnvoteableHost = "participant-a"
	rungModel          = "model-a"
)

type fixedUnvoteableHosts map[string]map[string]bool

func (f fixedUnvoteableHosts) UnvoteableHosts(model string) map[string]bool { return f[model] }

func rungUnvoteableHosts(hosts ...string) fixedUnvoteableHosts {
	named := make(map[string]bool, len(hosts))
	for _, host := range hosts {
		named[host] = true
	}
	return fixedUnvoteableHosts{rungModel: named}
}

func rungTracker(unvoteable bool) (*Tracker, *steppedClock, *recordingHostNarrator) {
	perf := noCap(testPerf())
	clock := &steppedClock{current: testEpoch}
	tracker := newTestTracker(perf, clock.now)
	narrator := &recordingHostNarrator{}
	tracker.SetNarrator(narrator)
	hosts := rungUnvoteableHosts()
	if unvoteable {
		hosts = rungUnvoteableHosts(rungUnvoteableHost)
	}
	tracker.SetUnvoteableSource(hosts)
	return tracker, clock, narrator
}

func climbToRung(tracker *Tracker, clock *steppedClock, rungs int) {
	perf := testPerf()
	for range rungs {
		failAllConsecutive(tracker, rungUnvoteableHost, rungModel, perf.ConsecutiveFailThreshold)
		clock.current = clock.current.Add(time.Duration(perf.EjectionMaxSeconds) * time.Second)
		tracker.RecordSample(Sample{ParticipantKey: rungUnvoteableHost, Model: rungModel, Responsive: true})
	}
}

func lastWithholding(narrator *recordingHostNarrator) Withholding {
	return narrator.withheld[len(narrator.withheld)-1]
}

// Test flow:
//  1. Eject an unvoteable host three times, reset the host stats, and trip the same host again.
//  2. Read the withholding the tracker narrated for that fourth ejection.
//  3. Assert it is rung 4 lasting 120 seconds, and that the failure run before it restarted from zero.
func TestAnUnvoteableHostEjectedAfterTheResetResumesItsRung(t *testing.T) {
	tracker, clock, narrator := rungTracker(true)
	climbToRung(tracker, clock, 3)
	require.Equal(t, 3, lastWithholding(narrator).EjectionCount)

	tracker.ResetHostStats()
	failAllConsecutive(tracker, rungUnvoteableHost, rungModel, testPerf().ConsecutiveFailThreshold-1)
	require.False(t, tracker.Ejected(rungUnvoteableHost, rungModel))
	failAllConsecutive(tracker, rungUnvoteableHost, rungModel, 1)

	require.Equal(t, 4, lastWithholding(narrator).EjectionCount)
	require.Equal(t, 120*time.Second, lastWithholding(narrator).WithheldFor)
	require.True(t, tracker.Ejected(rungUnvoteableHost, rungModel))
}

// Test flow:
//  1. Eject a host three times with no unvoteable host in the model, reset, and trip it again.
//  2. Let that withholding lapse, make the host unvoteable, and trip it a third time.
//  3. Assert the first ejection after the reset is rung 1 for 30 seconds and the next is rung 2, since the memory went with the first.
func TestAHostThatIsNotUnvoteableRestartsAtTheFirstRungAndLosesItsMemory(t *testing.T) {
	tracker, clock, narrator := rungTracker(false)
	climbToRung(tracker, clock, 3)

	tracker.ResetHostStats()
	failAllConsecutive(tracker, rungUnvoteableHost, rungModel, testPerf().ConsecutiveFailThreshold)
	require.Equal(t, 1, lastWithholding(narrator).EjectionCount)
	require.Equal(t, 30*time.Second, lastWithholding(narrator).WithheldFor)

	clock.current = clock.current.Add(time.Minute)
	tracker.SetUnvoteableSource(rungUnvoteableHosts(rungUnvoteableHost))
	tracker.RecordSample(Sample{ParticipantKey: rungUnvoteableHost, Model: rungModel, Responsive: true})
	failAllConsecutive(tracker, rungUnvoteableHost, rungModel, testPerf().ConsecutiveFailThreshold)

	require.Equal(t, 2, lastWithholding(narrator).EjectionCount)
	require.Equal(t, 60*time.Second, lastWithholding(narrator).WithheldFor)
}

// Test flow:
//  1. Eject an unvoteable host three times, reset, and feed it answered samples only.
//  2. Read its routing verdicts and the withholdings narrated.
//  3. Assert it is neither withheld nor degraded and nothing was narrated after the reset.
func TestAnUnvoteableHostNeverEjectedAgainKeepsACleanState(t *testing.T) {
	tracker, clock, narrator := rungTracker(true)
	climbToRung(tracker, clock, 3)
	withheldBefore := len(narrator.withheld)

	tracker.ResetHostStats()
	answeredSamples(tracker, rungUnvoteableHost, rungModel, 12)

	require.False(t, tracker.Ejected(rungUnvoteableHost, rungModel))
	require.False(t, tracker.Degraded(rungUnvoteableHost, rungModel))
	require.Len(t, narrator.withheld, withheldBefore)
	require.Len(t, tracker.Snapshot(), 1)
	require.False(t, tracker.Snapshot()[0].Ejected)
}

// Test flow:
//  1. Eject an unvoteable host three times, reset, answer a sample so the host exists again, and reset a second time.
//  2. Trip the unvoteable host.
//  3. Assert the ejection is rung 1 for 30 seconds, since the second reset replaced the memory with the fresh state.
func TestTheRungMemoryDoesNotSurviveASecondReset(t *testing.T) {
	tracker, clock, narrator := rungTracker(true)
	climbToRung(tracker, clock, 3)

	tracker.ResetHostStats()
	answeredSamples(tracker, rungUnvoteableHost, rungModel, 1)
	tracker.ResetHostStats()
	failAllConsecutive(tracker, rungUnvoteableHost, rungModel, testPerf().ConsecutiveFailThreshold)

	require.Equal(t, 1, lastWithholding(narrator).EjectionCount)
	require.Equal(t, 30*time.Second, lastWithholding(narrator).WithheldFor)
}

// Test flow:
//  1. Eject an unvoteable host three times, reset, and trip it so it resumes at rung 4.
//  2. Keep it healthy for one full decay window after the withholding lapses and trip it again.
//  3. Assert the rung relaxed by one and the next ejection is rung 4 again, not 5.
func TestAResumedRungDecaysLikeAnyOther(t *testing.T) {
	tracker, clock, narrator := rungTracker(true)
	climbToRung(tracker, clock, 3)
	tracker.ResetHostStats()
	failAllConsecutive(tracker, rungUnvoteableHost, rungModel, testPerf().ConsecutiveFailThreshold)
	require.Equal(t, 4, lastWithholding(narrator).EjectionCount)

	clock.current = clock.current.Add(120*time.Second + time.Duration(testPerf().EjectionMaxSeconds)*time.Second)
	tracker.RecordSample(Sample{ParticipantKey: rungUnvoteableHost, Model: rungModel, Responsive: true})
	failAllConsecutive(tracker, rungUnvoteableHost, rungModel, testPerf().ConsecutiveFailThreshold)

	require.Equal(t, 4, lastWithholding(narrator).EjectionCount)
}

// Test flow:
//  1. Eject an unvoteable host three times, reset, keep it healthy for a full decay window past the end of the rung it held, and trip it.
//  2. Read the narrated withholding.
//  3. Assert the healing time after the reset counts too, so the ejection is rung 3 and not 4.
func TestTheRememberedRungRelaxesByTheHealingTimeAfterTheReset(t *testing.T) {
	tracker, clock, narrator := rungTracker(true)
	climbToRung(tracker, clock, 3)

	tracker.ResetHostStats()
	clock.current = clock.current.Add(time.Duration(testPerf().EjectionMaxSeconds) * time.Second)
	answeredSamples(tracker, rungUnvoteableHost, rungModel, 1)
	failAllConsecutive(tracker, rungUnvoteableHost, rungModel, testPerf().ConsecutiveFailThreshold)

	require.Equal(t, 3, lastWithholding(narrator).EjectionCount)
}

// Test flow:
//  1. Eject an unvoteable host once, so its withholding is still running, and reset.
//  2. Trip the unvoteable host again at once.
//  3. Assert the ejection is rung 2 for 60 seconds, the full remembered rung plus one.
func TestAHostStillWithheldAtTheResetKeepsItsFullRung(t *testing.T) {
	tracker, _, narrator := rungTracker(true)
	failAllConsecutive(tracker, rungUnvoteableHost, rungModel, testPerf().ConsecutiveFailThreshold)

	tracker.ResetHostStats()
	failAllConsecutive(tracker, rungUnvoteableHost, rungModel, testPerf().ConsecutiveFailThreshold)

	require.Equal(t, 2, lastWithholding(narrator).EjectionCount)
	require.Equal(t, 60*time.Second, lastWithholding(narrator).WithheldFor)
}
