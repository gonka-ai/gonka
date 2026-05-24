package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// hostScoresTestRecord builds a 2-host record with explicit per-host timings.
func hostScoresTestRecord(host0TotalMs, host1TotalMs float64) RequestRecord {
	return RequestRecord{
		Timestamp:   time.Now(),
		Model:       "",
		InputTokens: 20_000,
		Hosts: []HostInvolvement{
			{HostIdx: 0, ParticipantKey: "host:0", FirstTokenMs: host0TotalMs / 10, TotalTimeMs: host0TotalMs, Responsive: true, Finished: true},
			{HostIdx: 1, ParticipantKey: "host:1", FirstTokenMs: host1TotalMs / 10, TotalTimeMs: host1TotalMs, Responsive: true, Finished: true},
		},
	}
}

func TestDecideHostScoreSpeedup_H2HCandidateBeatsPrimary(t *testing.T) {
	perf := NewPerfTracker(nil)
	// 4 records where host:1 always finishes 2x faster → H2H rate(host:1 vs host:0) = 1.0
	for i := 0; i < 4; i++ {
		perf.RecordRequest(hostScoresTestRecord(2000, 1000))
	}

	redundancy := &Redundancy{perf: perf, groupSize: 2}
	d := redundancy.decideHostScoreSpeedup(0, 20_000)
	require.True(t, d.RunSecondary)
	require.Equal(t, "host_scores", d.Reason)
	require.Equal(t, 1, d.ImmediateAttempts)
}

func TestDecideHostScoreSpeedup_EloLayerWhenNoH2H(t *testing.T) {
	perf := NewPerfTracker(nil)
	// Feed each host on its own so PairwiseTracker stays empty → Layer 1 skipped.
	for i := 0; i < 4; i++ {
		perf.RecordRequest(RequestRecord{
			Timestamp:   time.Now(),
			Model:       "",
			InputTokens: 20_000,
			Hosts: []HostInvolvement{
				{HostIdx: 0, ParticipantKey: "host:0", FirstTokenMs: 200, TotalTimeMs: 2000, Responsive: true, Finished: true},
			},
		})
	}
	for i := 0; i < 4; i++ {
		perf.RecordRequest(RequestRecord{
			Timestamp:   time.Now(),
			Model:       "",
			InputTokens: 20_000,
			Hosts: []HostInvolvement{
				{HostIdx: 1, ParticipantKey: "host:1", FirstTokenMs: 80, TotalTimeMs: 800, Responsive: true, Finished: true},
			},
		})
	}

	redundancy := &Redundancy{perf: perf, groupSize: 2}
	d := redundancy.decideHostScoreSpeedup(0, 20_000)
	require.True(t, d.RunSecondary, "Elo layer must trigger when H2H absent and candidate much faster")
	require.Equal(t, "host_scores", d.Reason)
	require.Equal(t, 1, d.ImmediateAttempts)
}

func TestDecideHostScoreSpeedup_ColdStartReturnsNoData(t *testing.T) {
	withForcedExploration(t, 0, 0) // isolate from ε-greedy floor
	perf := NewPerfTracker(nil)
	// Only one sample per host — below HostScoreMinSamples for both layers.
	perf.RecordRequest(hostScoresTestRecord(2000, 1000))

	redundancy := &Redundancy{perf: perf, groupSize: 2}
	d := redundancy.decideHostScoreSpeedup(0, 20_000)
	require.False(t, d.RunSecondary)
	require.Equal(t, "host_scores_no_data", d.Reason, "cold start (samples < MinSamples) must report no_data")
}

func TestDecideHostScoreSpeedup_CandidateTooCloseReturnsNoCandidate(t *testing.T) {
	withForcedExploration(t, 0, 0) // isolate from ε-greedy floor
	perf := NewPerfTracker(nil)
	// Both hosts within SpeedupMargin → we have data but no qualifier → no_candidate.
	for i := 0; i < 4; i++ {
		perf.RecordRequest(RequestRecord{
			Timestamp:   time.Now(),
			Model:       "",
			InputTokens: 20_000,
			Hosts: []HostInvolvement{
				{HostIdx: 0, ParticipantKey: "host:0", FirstTokenMs: 100, TotalTimeMs: 1000, Responsive: true, Finished: true},
			},
		})
		perf.RecordRequest(RequestRecord{
			Timestamp:   time.Now(),
			Model:       "",
			InputTokens: 20_000,
			Hosts: []HostInvolvement{
				{HostIdx: 1, ParticipantKey: "host:1", FirstTokenMs: 98, TotalTimeMs: 980, Responsive: true, Finished: true},
			},
		})
	}

	redundancy := &Redundancy{perf: perf, groupSize: 2}
	d := redundancy.decideHostScoreSpeedup(0, 20_000)
	require.False(t, d.RunSecondary)
	require.Equal(t, "host_scores_no_candidate", d.Reason, "had data, no candidate beat margin → no_candidate")
}

func TestDecideHostScoreSpeedup_GroupSizeOneReturnsNoData(t *testing.T) {
	perf := NewPerfTracker(nil)
	for i := 0; i < 4; i++ {
		perf.RecordRequest(hostScoresTestRecord(2000, 1000))
	}
	redundancy := &Redundancy{perf: perf, groupSize: 1}
	d := redundancy.decideHostScoreSpeedup(0, 20_000)
	require.False(t, d.RunSecondary)
	require.Equal(t, "host_scores_no_data", d.Reason)
}

func TestDecideHostScoreSpeedup_NilPerfReturnsNoData(t *testing.T) {
	redundancy := &Redundancy{groupSize: 2}
	d := redundancy.decideHostScoreSpeedup(0, 20_000)
	require.False(t, d.RunSecondary)
	require.Equal(t, "host_scores_no_data", d.Reason)
}

func withRedundancyPolicyForTest(t *testing.T, policy string) {
	t.Helper()
	saved := RedundancySpeedPolicy
	RedundancySpeedPolicy = policy
	t.Cleanup(func() { RedundancySpeedPolicy = saved })
}

func TestDecide_HostScorePolicyReturnsHostScoreDecision(t *testing.T) {
	withRedundancyPolicyForTest(t, RedundancySpeedPolicyHostScore)

	perf := NewPerfTracker(nil)
	for i := 0; i < 4; i++ {
		perf.RecordRequest(hostScoresTestRecord(2000, 1000))
	}

	redundancy := &Redundancy{perf: perf, groupSize: 2}
	d := redundancy.Decide(0, 20_000)
	require.True(t, d.RunSecondary)
	require.Equal(t, "host_scores", d.Reason, "host_score policy must delegate the decision to decideHostScoreSpeedup")
}

func TestDecide_NonHostScorePolicySkipsHostScore(t *testing.T) {
	withRedundancyPolicyForTest(t, RedundancySpeedPolicyHybrid)

	perf := NewPerfTracker(nil)
	for i := 0; i < 4; i++ {
		perf.RecordRequest(hostScoresTestRecord(2000, 1000))
	}

	redundancy := &Redundancy{perf: perf, groupSize: 2}
	d := redundancy.Decide(0, 20_000)
	require.NotEqual(t, "host_scores", d.Reason, "non-host-score policies must not select the host-score path")
}

func withForcedExploration(t *testing.T, epsilon, rollValue float64) {
	t.Helper()
	savedEps := HostScoreExplorationEpsilon
	savedRand := hostScoreRandom
	HostScoreExplorationEpsilon = epsilon
	hostScoreRandom = func() float64 { return rollValue }
	t.Cleanup(func() {
		HostScoreExplorationEpsilon = savedEps
		hostScoreRandom = savedRand
	})
}

func TestDecideHostScoreSpeedup_ExplorationFiresWhenScoreAmbivalent(t *testing.T) {
	withForcedExploration(t, 1.0, 0.0) // roll < ε → always explore

	perf := NewPerfTracker(nil)
	// No samples → score-based path has nothing to evaluate; exploration fires.
	redundancy := &Redundancy{perf: perf, groupSize: 2}
	d := redundancy.decideHostScoreSpeedup(0, 20_000)
	require.True(t, d.RunSecondary)
	require.Equal(t, "host_scores_exploration", d.Reason)
	require.Equal(t, 1, d.ImmediateAttempts)
}

func TestDecideHostScoreSpeedup_ExplorationDisabledByZeroEpsilon(t *testing.T) {
	withForcedExploration(t, 0.0, 0.0)

	perf := NewPerfTracker(nil)
	redundancy := &Redundancy{perf: perf, groupSize: 2}
	d := redundancy.decideHostScoreSpeedup(0, 20_000)
	require.False(t, d.RunSecondary)
	require.Equal(t, "host_scores_no_data", d.Reason, "ε=0 plus no samples must report no_data")
}

func TestDecideHostScoreSpeedup_ConfidentPickIgnoresExploration(t *testing.T) {
	// Roll always fires, but confident pick must take precedence over exploration.
	withForcedExploration(t, 1.0, 0.0)

	perf := NewPerfTracker(nil)
	for i := 0; i < 4; i++ {
		perf.RecordRequest(hostScoresTestRecord(2000, 1000))
	}

	redundancy := &Redundancy{perf: perf, groupSize: 2}
	d := redundancy.decideHostScoreSpeedup(0, 20_000)
	require.True(t, d.RunSecondary)
	require.Equal(t, "host_scores", d.Reason, "confident score-based pick must not be masked by exploration")
}

func TestDecideHostScoreSpeedup_ExplorationSkippedWhenRollAboveEpsilon(t *testing.T) {
	withForcedExploration(t, 0.05, 0.5) // roll > ε → no exploration

	perf := NewPerfTracker(nil)
	redundancy := &Redundancy{perf: perf, groupSize: 2}
	d := redundancy.decideHostScoreSpeedup(0, 20_000)
	require.False(t, d.RunSecondary)
	require.Equal(t, "host_scores_no_data", d.Reason, "roll above ε must not fire exploration; with no data result is no_data")
}

func TestDecide_HostScorePolicyDoesNotCascadeOnNoCandidate(t *testing.T) {
	// no_candidate must not be overridden by a legacy fallback.
	withRedundancyPolicyForTest(t, RedundancySpeedPolicyHostScore)
	withForcedExploration(t, 0, 0)

	perf := NewPerfTracker(nil)
	for i := 0; i < 4; i++ {
		perf.RecordRequest(RequestRecord{
			Timestamp:   time.Now(),
			Model:       "",
			InputTokens: 20_000,
			Hosts: []HostInvolvement{
				{HostIdx: 0, ParticipantKey: "host:0", FirstTokenMs: 100, TotalTimeMs: 1000, Responsive: true, Finished: true},
			},
		})
		perf.RecordRequest(RequestRecord{
			Timestamp:   time.Now(),
			Model:       "",
			InputTokens: 20_000,
			Hosts: []HostInvolvement{
				{HostIdx: 1, ParticipantKey: "host:1", FirstTokenMs: 98, TotalTimeMs: 980, Responsive: true, Finished: true},
			},
		})
	}

	redundancy := &Redundancy{perf: perf, groupSize: 2}
	d := redundancy.Decide(0, 20_000)
	require.False(t, d.RunSecondary)
	require.Equal(t, "host_scores_no_candidate", d.Reason)
}

func TestDecide_HostScorePolicyDoesNotCascadeOnNoData(t *testing.T) {
	// no_data must not cascade to legacy either; ε-greedy forced off.
	withRedundancyPolicyForTest(t, RedundancySpeedPolicyHostScore)
	withForcedExploration(t, 0, 0)

	perf := NewPerfTracker(nil)
	perf.RecordRequest(hostScoresTestRecord(2000, 1000))

	redundancy := &Redundancy{perf: perf, groupSize: 2}
	d := redundancy.Decide(0, 20_000)
	require.False(t, d.RunSecondary)
	require.Equal(t, "host_scores_no_data", d.Reason)
}

// withDeterministicPairwiseRandom silences pairwise AB-sample randomness for routing tests.
func withDeterministicPairwiseRandom(t *testing.T) {
	t.Helper()
	saved := pairwiseABRandom
	pairwiseABRandom = func() float64 { return 1 } // never fire AB sample
	t.Cleanup(func() { pairwiseABRandom = saved })
}

func TestDecide_PairwisePolicyRoutesToPairwise(t *testing.T) {
	// pairwise policy must never enter host-score path, even with host-score data.
	withRedundancyPolicyForTest(t, RedundancySpeedPolicyPairwise)
	withDeterministicPairwiseRandom(t)

	perf := NewPerfTracker(nil)
	for i := 0; i < 4; i++ {
		perf.RecordRequest(hostScoresTestRecord(2000, 1000))
	}

	redundancy := &Redundancy{perf: perf, groupSize: 2}
	d := redundancy.Decide(0, 20_000)
	require.NotContains(t, d.Reason, "host_scores", "pairwise policy must not enter host-score path")
	require.Contains(t, d.Reason, "pairwise", "pairwise policy must produce a pairwise_* reason")
}

func TestDecide_HybridPolicyRoutesPairwiseThenLegacy(t *testing.T) {
	// hybrid policy must never enter host-score path, even with host-score data.
	withRedundancyPolicyForTest(t, RedundancySpeedPolicyHybrid)
	withDeterministicPairwiseRandom(t)

	perf := NewPerfTracker(nil)
	for i := 0; i < 4; i++ {
		perf.RecordRequest(hostScoresTestRecord(2000, 1000))
	}

	redundancy := &Redundancy{perf: perf, groupSize: 2}
	d := redundancy.Decide(0, 20_000)
	require.NotContains(t, d.Reason, "host_scores", "hybrid policy must not enter the host-score path")
}
