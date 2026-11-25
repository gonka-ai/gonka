package perf

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func answeredSamples(tracker *Tracker, participant, model string, count int) {
	for range count {
		tracker.RecordSample(Sample{ParticipantKey: participant, Model: model, Responsive: true, FirstContent: time.Second, TimePerOutputToken: 50 * time.Millisecond})
	}
}

// Test flow:
//  1. Build a tracker with the cap off and a recording narrator, give participant-a/model-a ten answered samples and eject participant-b/model-a, with one in-flight acquire and one context limit recorded.
//  2. Reset the host stats.
//  3. Assert the withheld count, that a HostReturned was narrated for the withheld host, that the ejection and the latency signals are gone, and that the in-flight count and the capability survive.
func TestResetHostStatsForgetsEveryHostAndNarratesTheReturns(t *testing.T) {
	perf := noCap(testPerf())
	tracker := newTestTracker(perf, fixedNow(testEpoch))
	narrator := &recordingHostNarrator{}
	tracker.SetNarrator(narrator)
	answeredSamples(tracker, "participant-a", "model-a", 12)
	failAllConsecutive(tracker, "participant-b", "model-a", perf.ConsecutiveFailThreshold)
	tracker.Acquire("participant-a")
	tracker.RecordContextLimit("participant-a", "model-a", 4096)
	_, known := tracker.FirstContentP75("participant-a", "model-a")
	require.True(t, known, "the latency ring must be warm before the reset")
	require.True(t, tracker.Ejected("participant-b", "model-a"))

	withheld := tracker.ResetHostStats()

	require.Equal(t, 1, withheld)
	require.Equal(t, []recordedReturn{{participant: "participant-b", model: "model-a"}}, narrator.returned)
	require.False(t, tracker.Ejected("participant-b", "model-a"))
	require.False(t, tracker.Degraded("participant-b", "model-a"))
	_, known = tracker.FirstContentP75("participant-a", "model-a")
	require.False(t, known)
	require.Equal(t, Pressure{}, tracker.Pressure("participant-a", "model-a"))
	require.Empty(t, tracker.Snapshot())
	require.Equal(t, 1, tracker.inflight.count("participant-a"))
	limit, _, _, _ := tracker.Capability("participant-a", "model-a")
	require.Equal(t, uint64(4096), limit)
}

// Test flow:
//  1. Eject participant-a/model-a three times in a row so its rung climbs to 3, then reset the host stats.
//  2. Trip the same host again.
//  3. Assert the new withholding is rung 1 for the base 30 seconds, and that the consecutive-failure run restarted from zero.
func TestTheRungRestartsAtOneAfterAReset(t *testing.T) {
	perf := noCap(testPerf())
	clock := &steppedClock{current: testEpoch}
	tracker := newTestTracker(perf, clock.now)
	narrator := &recordingHostNarrator{}
	tracker.SetNarrator(narrator)
	for range 3 {
		failAllConsecutive(tracker, "participant-a", "model-a", perf.ConsecutiveFailThreshold)
		clock.current = clock.current.Add(time.Duration(perf.EjectionMaxSeconds) * time.Second)
		tracker.RecordSample(Sample{ParticipantKey: "participant-a", Model: "model-a", Responsive: true})
	}
	require.Equal(t, 3, narrator.withheld[len(narrator.withheld)-1].EjectionCount)

	tracker.ResetHostStats()
	failAllConsecutive(tracker, "participant-a", "model-a", perf.ConsecutiveFailThreshold-1)
	require.False(t, tracker.Ejected("participant-a", "model-a"), "the run of failures restarts from zero")
	failAllConsecutive(tracker, "participant-a", "model-a", 1)

	last := narrator.withheld[len(narrator.withheld)-1]
	require.Equal(t, 1, last.EjectionCount)
	require.Equal(t, 30*time.Second, last.WithheldFor)
}

// Test flow:
//  1. Start one goroutine recording samples, one reading Ejected and one resetting, all on the same tracker.
//  2. Let each run a fixed number of iterations.
//  3. Assert the run finishes with no data race under -race and the tracker stays readable.
func TestResetHostStatsRacesNeitherSamplesNorReaders(t *testing.T) {
	perf := noCap(testPerf())
	tracker := newTestTracker(perf, fixedNow(testEpoch))
	var group sync.WaitGroup
	const iterations = 500
	group.Add(3)
	go func() {
		defer group.Done()
		for range iterations {
			tracker.RecordSample(Sample{ParticipantKey: "participant-a", Model: "model-a", Responsive: false})
		}
	}()
	go func() {
		defer group.Done()
		for range iterations {
			tracker.Ejected("participant-a", "model-a")
			tracker.Degraded("participant-a", "model-a")
		}
	}()
	go func() {
		defer group.Done()
		for range iterations {
			tracker.ResetHostStats()
		}
	}()
	group.Wait()

	tracker.ResetHostStats()
	require.False(t, tracker.Ejected("participant-a", "model-a"))
}

type steppedClock struct {
	current time.Time
}

func (c *steppedClock) now() time.Time { return c.current }
