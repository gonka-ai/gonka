package main

import (
	"path/filepath"
	"testing"
	"time"

	"devshard/user"

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

// Accounting reports these verdicts per participant, where there is no request to judge against, so
// they have to be readable on their own rather than only through HostCannotServeRequest.
func TestPerfTrackerCapabilityBlocksReportsEveryObservedLimit(t *testing.T) {
	perf := NewPerfTracker(nil)
	perf.RecordVersionUnsupported("p1")
	perf.RecordToolUnsupported("p1", "m")
	perf.RecordContextLimit("p2", "m", 4096)

	protocolVersion, toolChoice, contextLimit := perf.CapabilityBlocks("p1", "m")
	require.True(t, protocolVersion)
	require.True(t, toolChoice)
	require.Zero(t, contextLimit)

	protocolVersion, toolChoice, contextLimit = perf.CapabilityBlocks("p2", "m")
	require.False(t, protocolVersion)
	require.False(t, toolChoice)
	require.Equal(t, uint64(4096), contextLimit)

	protocolVersion, toolChoice, contextLimit = perf.CapabilityBlocks("never-seen", "m")
	require.False(t, protocolVersion)
	require.False(t, toolChoice)
	require.Zero(t, contextLimit)
}

func TestPerfTrackerHostCannotServeRequestUsesCapabilities(t *testing.T) {
	perf := NewPerfTracker(nil)
	perf.RecordToolUnsupported("p1", "m")
	perf.RecordContextLimit("p2", "m", 1000)

	toolParams := user.InferenceParams{Model: "m", Prompt: []byte(`{"messages":[{"role":"user","content":"x"}],"tools":[{"type":"function"}],"tool_choice":"auto"}`), InputLength: 10}
	reason, blocked := perf.HostCannotServeRequest("p1", toolParams)
	require.True(t, blocked)
	require.Equal(t, "tool_choice_unsupported", reason)

	// Do not guess from gateway-estimated input length or max_tokens. Context
	// routing only activates after this request gets the upstream total hint.
	unhintedParams := user.InferenceParams{Model: "m", Prompt: []byte(`{"messages":[{"role":"user","content":"x"}]}`), InputLength: 900, MaxTokens: 101}
	_, blocked = perf.HostCannotServeRequest("p2", unhintedParams)
	require.False(t, blocked)

	smallParams := user.InferenceParams{Model: "m", Prompt: []byte(`{"messages":[{"role":"user","content":"x"}]}`), ContextTotalHint: 1000}
	_, blocked = perf.HostCannotServeRequest("p2", smallParams)
	require.False(t, blocked)

	hintedParams := user.InferenceParams{Model: "m", Prompt: []byte(`{"messages":[{"role":"user","content":"x"}]}`), InputLength: 10, MaxTokens: 10, ContextTotalHint: 1001}
	reason, blocked = perf.HostCannotServeRequest("p2", hintedParams)
	require.True(t, blocked)
	require.Equal(t, "context_limit_exceeded", reason)
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
	for i := 0; i < 100; i++ {
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

// Context length belongs to the model, not the host: a participant serving three models has three of
// them. Keying by participant alone let a limit learned on one model block requests to another.
func TestPerfTrackerCapabilities_AreKeptPerModel(t *testing.T) {
	perf := NewPerfTracker(nil)
	perf.RecordContextLimit("p1", "small-context", 4096)
	perf.RecordToolUnsupported("p1", "no-tools")

	_, _, small := perf.CapabilityBlocks("p1", "small-context")
	require.Equal(t, uint64(4096), small)
	_, _, other := perf.CapabilityBlocks("p1", "big-context")
	require.Zero(t, other, "a limit learned on one model says nothing about another")

	_, toolsBlocked, _ := perf.CapabilityBlocks("p1", "no-tools")
	require.True(t, toolsBlocked)
	_, toolsElsewhere, _ := perf.CapabilityBlocks("p1", "small-context")
	require.False(t, toolsElsewhere)
}

// A request to the model whose limit was never learned must not be blocked by another model's.
func TestPerfTrackerHostCannotServeRequest_HonoursTheRequestedModel(t *testing.T) {
	perf := NewPerfTracker(nil)
	perf.RecordContextLimit("p1", "small-context", 1000)

	oversized := user.InferenceParams{Model: "small-context", Prompt: []byte(`{"messages":[]}`), ContextTotalHint: 5000}
	reason, blocked := perf.HostCannotServeRequest("p1", oversized)
	require.True(t, blocked)
	require.Equal(t, "context_limit_exceeded", reason)

	sameSizeOtherModel := user.InferenceParams{Model: "big-context", Prompt: []byte(`{"messages":[]}`), ContextTotalHint: 5000}
	_, blocked = perf.HostCannotServeRequest("p1", sameSizeOtherModel)
	require.False(t, blocked, "the other model's limit is unknown, not exceeded")
}

// The build is one per host, so the protocol verdict stays keyed by participant and covers every model.
func TestPerfTrackerVersionVerdict_CoversEveryModel(t *testing.T) {
	perf := NewPerfTracker(nil)
	perf.RecordVersionUnsupported("p1")

	for _, model := range []string{"a", "b", ""} {
		blocked, _, _ := perf.CapabilityBlocks("p1", model)
		require.True(t, blocked, "model %q", model)
	}
}
