package perf

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func failedLaunchedAt(tracker *Tracker, participant, model string, launched time.Time, count int64) {
	for range count {
		tracker.RecordSample(Sample{ParticipantKey: participant, Model: model, Responsive: false, LaunchedAt: launched})
	}
}

// Test flow:
//  1. Build a tracker on a stepped clock and note an instant, then step the clock a minute and reset the host stats.
//  2. Record a failing sample for a host at the ejection threshold, launched before the reset, and a second run launched at the reset instant.
//  3. Assert the samples launched before the reset leave no host state and no ejection, and the ones launched at the reset instant count and eject.
func TestASampleOfAnAttemptLaunchedBeforeTheResetIsDropped(t *testing.T) {
	perf := noCap(testPerf())
	clock := &steppedClock{current: testEpoch}
	tracker := newTestTracker(perf, clock.now)
	launched := clock.current
	clock.current = clock.current.Add(time.Minute)
	tracker.ResetHostStats()

	failedLaunchedAt(tracker, "participant-a", "model-a", launched, perf.ConsecutiveFailThreshold)
	require.False(t, tracker.Ejected("participant-a", "model-a"), "failures of attempts launched before the reset")
	require.Empty(t, tracker.Snapshot(), "host state the dropped samples left")

	failedLaunchedAt(tracker, "participant-a", "model-a", clock.current, perf.ConsecutiveFailThreshold)
	require.True(t, tracker.Ejected("participant-a", "model-a"), "failures of attempts launched at the reset")
}

// Test flow:
//  1. Build a tracker on a stepped clock, step it a minute and reset the host stats.
//  2. Record a failing sample with no launch time at the ejection threshold.
//  3. Assert the host is ejected: a sample that carries no launch time is judged by when it arrives, as before.
func TestASampleWithNoLaunchTimeStillCountsAfterAReset(t *testing.T) {
	perf := noCap(testPerf())
	clock := &steppedClock{current: testEpoch}
	tracker := newTestTracker(perf, clock.now)
	clock.current = clock.current.Add(time.Minute)
	tracker.ResetHostStats()

	failAllConsecutive(tracker, "participant-a", "model-a", perf.ConsecutiveFailThreshold)

	require.True(t, tracker.Ejected("participant-a", "model-a"))
}
