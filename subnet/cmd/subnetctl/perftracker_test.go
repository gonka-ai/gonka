package main

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPerfTrackerAggregatesParticipantAcrossHostSlots(t *testing.T) {
	perf := NewPerfTracker(nil)
	now := time.Now()

	perf.Record(RequestSample{HostIdx: 0, ParticipantKey: "participant-a", Responsive: false, SendTime: now})
	perf.Record(RequestSample{HostIdx: 1, ParticipantKey: "participant-a", Responsive: true, SendTime: now})

	stats := perf.StatsForParticipant("participant-a")
	require.Equal(t, 2, stats.TotalSamples)
	require.Equal(t, 1, stats.FailureSamples)
	require.Equal(t, 0.5, stats.ResponsiveRate)
}

func TestParticipantPerfWindowUsesDeterministicJitter(t *testing.T) {
	saved := ParticipantPerfWindow
	ParticipantPerfWindow = time.Minute
	t.Cleanup(func() { ParticipantPerfWindow = saved })

	key := "gonka1participant"
	now := time.Unix(3_600, 0)
	windowStart := participantPerfWindowStart(key, now)
	require.Equal(t, participantPerfWindowOffset(key), participantPerfWindowOffset(key))
	require.Equal(t, windowStart, participantPerfWindowStart(key, now))

	perf := NewPerfTracker(nil)
	perf.Record(RequestSample{HostIdx: 0, ParticipantKey: key, Responsive: false, SendTime: windowStart.Add(-time.Nanosecond)})
	perf.Record(RequestSample{HostIdx: 0, ParticipantKey: key, Responsive: true, SendTime: windowStart})

	stats := perf.statsForKey(key, -1, now)
	require.Equal(t, 1, stats.TotalSamples)
	require.Zero(t, stats.FailureSamples)
}

func TestParticipantFailureThreshold(t *testing.T) {
	perf := NewPerfTracker(nil)
	now := time.Now()
	key := "participant-threshold"

	for i := 0; i < 99; i++ {
		perf.Record(RequestSample{HostIdx: 0, ParticipantKey: key, Responsive: true, SendTime: now})
	}
	perf.Record(RequestSample{HostIdx: 0, ParticipantKey: key, Responsive: false, SendTime: now})
	require.False(t, perf.ParticipantFailureThresholdExceeded(key), "1/100 is not more than 1 percent")

	perf.Record(RequestSample{HostIdx: 0, ParticipantKey: key, Responsive: false, SendTime: now})
	require.True(t, perf.ParticipantFailureThresholdExceeded(key), "2 failures crosses both short and 100-sample thresholds")
}

func TestPerfStoreBackfillsLegacyEscrowSamples(t *testing.T) {
	dir := t.TempDir()
	legacy, err := NewPerfStore(filepath.Join(dir, "escrow-12-state.db"))
	require.NoError(t, err)
	require.NoError(t, legacy.InsertSample(RequestSample{
		HostIdx:     1,
		Responsive:  false,
		SendTime:    time.Now(),
		ReceiptTime: time.Now(),
		InputTokens: 100,
	}))
	require.NoError(t, legacy.Close())

	globalStore, err := NewPerfStore(filepath.Join(dir, "perf.db"))
	require.NoError(t, err)
	defer globalStore.Close()

	perf := NewPerfTracker(globalStore)
	require.NoError(t, perf.BackfillLegacyEscrowSamples("12", filepath.Join(dir, "escrow-12-state.db"), []string{"participant-a", "participant-b"}))

	stats := perf.StatsForParticipant("participant-b")
	require.Equal(t, 1, stats.TotalSamples)
	require.Equal(t, 1, stats.FailureSamples)

	require.NoError(t, perf.BackfillLegacyEscrowSamples("12", filepath.Join(dir, "escrow-12-state.db"), []string{"participant-a", "participant-b"}))
	require.Equal(t, 1, perf.StatsForParticipant("participant-b").TotalSamples, "backfill should be idempotent")
}
