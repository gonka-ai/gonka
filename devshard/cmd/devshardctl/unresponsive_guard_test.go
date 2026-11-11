package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestStatsUnresponsiveRequiresMinSamplesForDecision(t *testing.T) {
	savedThreshold := UnresponsiveThreshold
	savedMinSamples := MinSamplesForDecision
	UnresponsiveThreshold = 0.8
	MinSamplesForDecision = 3
	t.Cleanup(func() {
		UnresponsiveThreshold = savedThreshold
		MinSamplesForDecision = savedMinSamples
	})

	perf := NewPerfTracker(nil)

	// Single failing sample in a fresh window must not mark a healthy host unresponsive.
	perf.Record(RequestSample{HostIdx: 0, ParticipantKey: "p-fast", Responsive: false})
	require.False(t, perf.IsUnresponsive(0))
	require.False(t, perf.IsUnresponsiveParticipant("p-fast"))

	// Two failing samples are still below MinSamplesForDecision=3.
	perf.Record(RequestSample{HostIdx: 0, ParticipantKey: "p-fast", Responsive: false})
	require.False(t, perf.IsUnresponsive(0))
	require.False(t, perf.IsUnresponsiveParticipant("p-fast"))

	// At MinSamplesForDecision=3 with 0/3 responsive (0.0 < 0.8), host is unresponsive.
	perf.Record(RequestSample{HostIdx: 0, ParticipantKey: "p-fast", Responsive: false})
	require.True(t, perf.IsUnresponsive(0))
	require.True(t, perf.IsUnresponsiveParticipant("p-fast"))
}

func TestDecidePrimaryUnresponsiveIgnoresSingleStrikeWithDefaultSettings(t *testing.T) {
	savedThreshold := UnresponsiveThreshold
	savedMinSamples := MinSamplesForDecision
	savedPolicy := RedundancySpeedPolicy
	ApplyRedundancySettings(DefaultRedundancySettings())
	MinSamplesForDecision = 3
	RedundancySpeedPolicy = RedundancySpeedPolicyLegacy
	t.Cleanup(func() {
		UnresponsiveThreshold = savedThreshold
		MinSamplesForDecision = savedMinSamples
		RedundancySpeedPolicy = savedPolicy
	})

	require.InDelta(t, 0.8, DefaultRedundancySettings().UnresponsiveThreshold, 1e-9)
	require.InDelta(t, 0.8, UnresponsiveThreshold, 1e-9)

	perf := NewPerfTracker(nil)
	now := time.Now()
	// Reproduce #2103: 16 responsive + 1 failure (responsive_rate = 16/17 = 0.941)
	// must NOT trip Rule 1 ("primary_unresponsive").
	perf.Record(RequestSample{HostIdx: 0, Responsive: false, SendTime: now})
	for i := 0; i < 16; i++ {
		perf.Record(RequestSample{
			HostIdx:     0,
			Responsive:  true,
			SendTime:    now,
			ReceiptTime: now.Add(10 * time.Millisecond),
		})
	}

	redundancy := &Redundancy{perf: perf, groupSize: 3}
	decision := redundancy.Decide(0, 100)
	require.NotEqual(t, "primary_unresponsive", decision.Reason)
}
