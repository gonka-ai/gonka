package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func withHostScorePolicy(t *testing.T) {
	t.Helper()
	saved := RedundancySpeedPolicy
	t.Cleanup(func() { RedundancySpeedPolicy = saved })
	RedundancySpeedPolicy = RedundancySpeedPolicyHostScore
}

func newLimiterForTest() *ParticipantRequestLimiter {
	return NewParticipantRequestLimiter(0, 0)
}

// seedQualifyingCohort fills the bucket with `count` other hosts, each
// contributing `samples` TTFTs at ttftMs. The candidate host (passed
// separately to recordSample) is compared against this cohort only —
// see bucketThresholdLocked's excludeHost logic.
func seedQualifyingCohort(l *ParticipantRequestLimiter, bucket string, count int, samples int, ttftMs float64) {
	for i := 0; i < count; i++ {
		host := "seed-" + string(rune('a'+i))
		for j := 0; j < samples; j++ {
			l.RecordHostScoreSample(host, bucket, ttftMs, true)
		}
	}
}

func TestHostScoreQuarantine_NoOpUnderNonHostScorePolicy(t *testing.T) {
	saved := RedundancySpeedPolicy
	t.Cleanup(func() { RedundancySpeedPolicy = saved })
	RedundancySpeedPolicy = RedundancySpeedPolicyHybrid

	l := newLimiterForTest()
	for i := 0; i < 100; i++ {
		l.RecordHostScoreSample("slow-host", "lt_1k", 100_000, true)
	}
	require.False(t, l.hostScoreQuar.isQuarantined("slow-host"))
}

func TestHostScoreQuarantine_NoThresholdUntilCohortQualifies(t *testing.T) {
	withHostScorePolicy(t)
	l := newLimiterForTest()

	// Only 2 qualifying hosts (need ≥ HostScoreQuarantineMinQualifyingHosts=3).
	seedQualifyingCohort(l, "lt_1k", 2, HostScoreQuarantineMinSamplesPerHost, 500)
	for i := 0; i < hostScoreSlidingWindow; i++ {
		l.RecordHostScoreSample("bad", "lt_1k", 100_000, true)
	}
	require.False(t, l.hostScoreQuar.isQuarantined("bad"),
		"threshold not computed yet — no quarantine should fire")
}

func TestHostScoreQuarantine_QuarantinesAfterBadInWindow(t *testing.T) {
	withHostScorePolicy(t)
	l := newLimiterForTest()

	seedQualifyingCohort(l, "lt_1k", 5, HostScoreQuarantineMinSamplesPerHost, 500)

	// Send HostScoreQuarantineBadInWindow (=3) slow samples — should trip quarantine.
	for i := 0; i < HostScoreQuarantineBadInWindow; i++ {
		l.RecordHostScoreSample("bad", "lt_1k", 60_000, true)
	}
	require.True(t, l.hostScoreQuar.isQuarantined("bad"))
}

func TestHostScoreQuarantine_BimodalCaughtBySlidingWindow(t *testing.T) {
	withHostScorePolicy(t)
	l := newLimiterForTest()

	seedQualifyingCohort(l, "lt_1k", 5, HostScoreQuarantineMinSamplesPerHost, 500)

	// Pattern: slow, slow, fast, slow → 3 bad in last 4 → quarantine.
	// (Old consecutive-3 strike model would NOT have caught this.)
	l.RecordHostScoreSample("bimodal", "lt_1k", 60_000, true) // slow
	l.RecordHostScoreSample("bimodal", "lt_1k", 60_000, true) // slow
	l.RecordHostScoreSample("bimodal", "lt_1k", 500, true)    // fast — would reset consecutive
	require.False(t, l.hostScoreQuar.isQuarantined("bimodal"))

	l.RecordHostScoreSample("bimodal", "lt_1k", 60_000, true) // slow → 3 of last 4 bad
	require.True(t, l.hostScoreQuar.isQuarantined("bimodal"))
}

func TestHostScoreQuarantine_PerBucketStateIsolated(t *testing.T) {
	withHostScorePolicy(t)
	l := newLimiterForTest()

	seedQualifyingCohort(l, "lt_1k", 5, HostScoreQuarantineMinSamplesPerHost, 500)
	seedQualifyingCohort(l, "1k_5k", 5, HostScoreQuarantineMinSamplesPerHost, 800)

	// Two strikes in lt_1k, two in 1k_5k — neither bucket alone has 3 bad-in-5.
	for i := 0; i < 2; i++ {
		l.RecordHostScoreSample("h", "lt_1k", 60_000, true)
		l.RecordHostScoreSample("h", "1k_5k", 60_000, true)
	}
	require.False(t, l.hostScoreQuar.isQuarantined("h"))

	// Third slow in lt_1k → that bucket alone trips → host marked overall.
	l.RecordHostScoreSample("h", "lt_1k", 60_000, true)
	require.True(t, l.hostScoreQuar.isQuarantined("h"))
}

func TestHostScoreQuarantine_UnresponsiveSamplesIgnored(t *testing.T) {
	withHostScorePolicy(t)
	l := newLimiterForTest()

	seedQualifyingCohort(l, "lt_1k", 5, HostScoreQuarantineMinSamplesPerHost, 500)

	// Unresponsive samples should not contribute to outcome window.
	for i := 0; i < 10; i++ {
		l.RecordHostScoreSample("h", "lt_1k", 0, false)
	}
	require.False(t, l.hostScoreQuar.isQuarantined("h"))
}

func TestHostScoreQuarantine_ExpiryUnlocksHost(t *testing.T) {
	withHostScorePolicy(t)
	l := newLimiterForTest()

	// Frozen clock so the test deterministically traverses expiry.
	clock := time.Now()
	l.hostScoreQuar.now = func() time.Time { return clock }

	seedQualifyingCohort(l, "lt_1k", 5, HostScoreQuarantineMinSamplesPerHost, 500)
	for i := 0; i < HostScoreQuarantineBadInWindow; i++ {
		l.RecordHostScoreSample("h", "lt_1k", 60_000, true)
	}
	require.True(t, l.hostScoreQuar.isQuarantined("h"))

	// Advance past base duration → next call processes expiry path.
	clock = clock.Add(HostScoreQuarantineBaseDuration + time.Second)
	l.RecordHostScoreSample("h", "lt_1k", 500, true)
	require.False(t, l.hostScoreQuar.isQuarantined("h"))
}

func TestHostScoreQuarantine_ExponentialBackoff(t *testing.T) {
	withHostScorePolicy(t)
	l := newLimiterForTest()

	clock := time.Now()
	l.hostScoreQuar.now = func() time.Time { return clock }
	seedQualifyingCohort(l, "lt_1k", 5, HostScoreQuarantineMinSamplesPerHost, 500)

	trip := func() time.Time {
		for i := 0; i < HostScoreQuarantineBadInWindow; i++ {
			l.RecordHostScoreSample("h", "lt_1k", 60_000, true)
		}
		st := l.hostScoreQuar.states["lt_1k"]["h"]
		return st.quarantineUntil
	}

	// First trip → base duration.
	end1 := trip()
	require.Equal(t, HostScoreQuarantineBaseDuration, end1.Sub(clock))

	// Advance just past end of first quarantine, trip again → 2× base.
	clock = end1.Add(time.Second)
	end2 := trip()
	require.Equal(t, 2*HostScoreQuarantineBaseDuration, end2.Sub(clock))

	// Third trip soon after → 4× base, capped at Max.
	clock = end2.Add(time.Second)
	end3 := trip()
	expected := 4 * HostScoreQuarantineBaseDuration
	if expected > HostScoreQuarantineMaxDuration {
		expected = HostScoreQuarantineMaxDuration
	}
	require.Equal(t, expected, end3.Sub(clock))
}

func TestHostScoreQuarantine_BackoffResetsAfterHistoryWindow(t *testing.T) {
	withHostScorePolicy(t)
	l := newLimiterForTest()

	clock := time.Now()
	l.hostScoreQuar.now = func() time.Time { return clock }
	seedQualifyingCohort(l, "lt_1k", 5, HostScoreQuarantineMinSamplesPerHost, 500)

	for i := 0; i < HostScoreQuarantineBadInWindow; i++ {
		l.RecordHostScoreSample("h", "lt_1k", 60_000, true)
	}
	end1 := l.hostScoreQuar.states["lt_1k"]["h"].quarantineUntil
	require.Equal(t, HostScoreQuarantineBaseDuration, end1.Sub(clock))

	// Advance past history window — old entry pruned, next trip starts fresh.
	clock = end1.Add(HostScoreQuarantineHistoryWindow + time.Minute)
	for i := 0; i < HostScoreQuarantineBadInWindow; i++ {
		l.RecordHostScoreSample("h", "lt_1k", 60_000, true)
	}
	end2 := l.hostScoreQuar.states["lt_1k"]["h"].quarantineUntil
	require.Equal(t, HostScoreQuarantineBaseDuration, end2.Sub(clock),
		"old quarantine outside history window → backoff resets to base")
}

func TestHostScoreQuarantine_FoldedIntoIsRecentlyQuarantined(t *testing.T) {
	withHostScorePolicy(t)
	l := newLimiterForTest()

	seedQualifyingCohort(l, "lt_1k", 5, HostScoreQuarantineMinSamplesPerHost, 500)
	for i := 0; i < HostScoreQuarantineBadInWindow; i++ {
		l.RecordHostScoreSample("bad", "lt_1k", 60_000, true)
	}
	require.True(t, l.IsRecentlyQuarantined("bad"))
	require.False(t, l.IsRecentlyQuarantined("seed-a"))
}

func TestHostScoreQuarantine_ClearQuarantineClearsLayer3(t *testing.T) {
	withHostScorePolicy(t)
	l := newLimiterForTest()

	seedQualifyingCohort(l, "lt_1k", 5, HostScoreQuarantineMinSamplesPerHost, 500)
	for i := 0; i < HostScoreQuarantineBadInWindow; i++ {
		l.RecordHostScoreSample("bad", "lt_1k", 60_000, true)
	}
	require.True(t, l.IsRecentlyQuarantined("bad"))

	require.True(t, l.ClearQuarantine("bad"), "ClearQuarantine should report it cleared something")
	require.False(t, l.IsRecentlyQuarantined("bad"))
}

func TestHostScoreQuarantine_WiredFromPerfTracker(t *testing.T) {
	withHostScorePolicy(t)
	l := newLimiterForTest()
	perf := NewPerfTracker(nil)
	perf.SetParticipantLimiter(l)

	for i := 0; i < HostScoreQuarantineMinQualifyingHosts; i++ {
		host := "seed-" + string(rune('a'+i))
		for j := 0; j < HostScoreQuarantineMinSamplesPerHost; j++ {
			perf.RecordRequest(mkRequest("m", 100, mkInvolvement(host, 500, 1000)))
		}
	}
	for i := 0; i < HostScoreQuarantineBadInWindow; i++ {
		perf.RecordRequest(mkRequest("m", 100, mkInvolvement("bad", 60_000, 120_000)))
	}
	require.True(t, l.IsRecentlyQuarantined("bad"))
	require.False(t, l.IsRecentlyQuarantined("seed-a"))
}
