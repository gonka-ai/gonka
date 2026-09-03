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

	for range 99 {
		perf.Record(RequestSample{HostIdx: 0, ParticipantKey: key, Responsive: true, SendTime: now})
	}
	perf.Record(RequestSample{HostIdx: 0, ParticipantKey: key, Responsive: false, SendTime: now})
	require.False(t, perf.ParticipantFailureThresholdExceeded(key), "1/100 is not more than 1 percent")

	perf.Record(RequestSample{HostIdx: 0, ParticipantKey: key, Responsive: false, SendTime: now})
	require.True(t, perf.ParticipantFailureThresholdExceeded(key), "2 failures crosses both short and 100-sample thresholds")
}

func TestPerfTrackerFirstTokenFallbackUsesP95AfterFullBucket(t *testing.T) {
	perf := NewPerfTracker(nil)
	for i := 1; i <= 99; i++ {
		perf.RecordRequest(RequestRecord{
			Model:       "Qwen/Test",
			InputTokens: 20_000,
			Hosts: []HostInvolvement{{
				FirstTokenMs: float64(i),
				Responsive:   true,
				Finished:     true,
				Winner:       true,
			}},
		})
	}
	_, ok := perf.FirstTokenFallbackDelay("Qwen/Test", 20_000)
	require.False(t, ok)

	perf.RecordRequest(RequestRecord{
		Model:       "Qwen/Test",
		InputTokens: 20_000,
		Hosts: []HostInvolvement{{
			FirstTokenMs: 100,
			Responsive:   true,
			Finished:     true,
			Winner:       true,
		}},
	})
	delay, ok := perf.FirstTokenFallbackDelay("Qwen/Test", 20_000)
	require.True(t, ok)
	require.Equal(t, 95*time.Millisecond, delay)
}

func TestPerfTrackerFirstTokenFallbackBucketsByModelAndInputSize(t *testing.T) {
	perf := NewPerfTracker(nil)
	for range 100 {
		perf.RecordRequest(RequestRecord{
			Model:       "Qwen/Test",
			InputTokens: 20_000,
			Hosts: []HostInvolvement{{
				FirstTokenMs: 100,
				Responsive:   true,
				Finished:     true,
				Winner:       true,
			}},
		})
	}

	delay, ok := perf.FirstTokenFallbackDelay("Qwen/Test", 20_000)
	require.True(t, ok)
	require.Equal(t, 100*time.Millisecond, delay)

	_, ok = perf.FirstTokenFallbackDelay("Qwen/Test", 100_000)
	require.False(t, ok, "different input bucket should not reuse the 16K-32K history")
	_, ok = perf.FirstTokenFallbackDelay("Kimi/Test", 20_000)
	require.False(t, ok, "different model should not reuse Qwen history")
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

// A refusal is charged to the nonce that met it, so the tracker keeps a count rather than a verdict:
// two refusals must read as two, not as a boolean that latched on the first.
func TestCapabilityRefusals_CountEveryObservation(t *testing.T) {
	perf := NewPerfTracker(nil)
	perf.RecordVersionUnsupported("p1")
	perf.RecordVersionUnsupported("p1")
	perf.RecordToolUnsupported("p1", "m")
	perf.RecordContextLimit("p1", "m", 4096)

	version, tool, context, limit := perf.CapabilityRefusals("p1", "m")

	require.Equal(t, uint64(2), version)
	require.Equal(t, uint64(1), tool)
	require.Equal(t, uint64(1), context)
	require.Equal(t, uint64(4096), limit)
}

// Context length and tool support belong to the model; the build is one per host and covers all of them.
func TestCapabilityRefusals_AreScopedLikeTheFactsThemselves(t *testing.T) {
	perf := NewPerfTracker(nil)
	perf.RecordVersionUnsupported("p1")
	perf.RecordToolUnsupported("p1", "no-tools")
	perf.RecordContextLimit("p1", "small", 4096)

	_, toolElsewhere, contextElsewhere, limitElsewhere := perf.CapabilityRefusals("p1", "other")
	require.Zero(t, toolElsewhere, "a tool refusal on one model says nothing about another")
	require.Zero(t, contextElsewhere)
	require.Zero(t, limitElsewhere)

	for _, model := range []string{"no-tools", "small", "other", ""} {
		version, _, _, _ := perf.CapabilityRefusals("p1", model)
		require.Equal(t, uint64(1), version, "model %q: the build covers every model", model)
	}

	version, _, _, _ := perf.CapabilityRefusals("never-seen", "m")
	require.Zero(t, version)
}
