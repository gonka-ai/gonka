package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func withPairwisePolicyForTest(t *testing.T) {
	t.Helper()
	savedPolicy := RedundancySpeedPolicy
	savedPercentile := PairwiseBudgetPercentile
	savedMaxAttempts := PairwiseMaxProactiveAttempts
	savedMinDirect := PairwiseMinDirectComparisons
	savedABSampleRate := PairwiseABSampleRate
	savedABSparseSampleRate := PairwiseABSparseSampleRate
	savedABSparseSampleThreshold := PairwiseABSparseSampleThreshold
	savedABRandom := pairwiseABRandom
	RedundancySpeedPolicy = RedundancySpeedPolicyPairwise
	PairwiseBudgetPercentile = 0.50
	PairwiseMaxProactiveAttempts = 3
	PairwiseMinDirectComparisons = 4
	PairwiseABSampleRate = 0
	PairwiseABSparseSampleRate = 0
	PairwiseABSparseSampleThreshold = 3
	pairwiseABRandom = func() float64 { return 1 }
	t.Cleanup(func() {
		RedundancySpeedPolicy = savedPolicy
		PairwiseBudgetPercentile = savedPercentile
		PairwiseMaxProactiveAttempts = savedMaxAttempts
		PairwiseMinDirectComparisons = savedMinDirect
		PairwiseABSampleRate = savedABSampleRate
		PairwiseABSparseSampleRate = savedABSparseSampleRate
		PairwiseABSparseSampleThreshold = savedABSparseSampleThreshold
		pairwiseABRandom = savedABRandom
	})
}

func TestPairwiseTrackerRecordsRequestComparisons(t *testing.T) {
	perf := NewPerfTracker(nil)
	for i := 0; i < 4; i++ {
		perf.RecordRequest(pairwiseTestRecord())
	}

	summaries := perf.PairwiseSummaries()
	require.NotEmpty(t, summaries)
	var found bool
	for _, summary := range summaries {
		if summary.ParticipantA == "host:0" && summary.ParticipantB == "host:1" {
			found = true
			require.Equal(t, 4, summary.SampleCount)
			require.InEpsilon(t, 2.0, summary.AvgRatioAToB, 0.001)
			require.InEpsilon(t, 0.5, summary.AvgSpeedupAToB, 0.001)
		}
	}
	require.True(t, found, "expected host:0 to host:1 comparison")
}

func TestDecision_UsesPairwiseBudgetedSpeedup(t *testing.T) {
	withPairwisePolicyForTest(t)
	perf := NewPerfTracker(nil)
	for i := 0; i < 4; i++ {
		perf.RecordRequest(pairwiseTestRecord())
	}

	redundancy := &Redundancy{perf: perf, groupSize: 2}
	d := redundancy.Decide(0, 20_000)

	require.True(t, d.RunSecondary)
	require.Equal(t, time.Duration(0), d.Delay)
	require.Equal(t, "pairwise_budgeted_speedup", d.Reason)
	require.Equal(t, 1, d.ImmediateAttempts)
}

func TestPairwiseDecisionSkipsUnavailableCandidate(t *testing.T) {
	withPairwisePolicyForTest(t)
	perf := NewPerfTracker(nil)
	for i := 0; i < 4; i++ {
		perf.RecordRequest(pairwiseTestRecord())
	}
	limiter := NewParticipantRequestLimiter(10, 10)
	limiter.ObserveStalledWinner("host:1")

	redundancy := &Redundancy{perf: perf, groupSize: 2, participantLimiter: limiter}
	d, ok := redundancy.decidePairwiseSpeedup(0, 20_000)

	require.False(t, ok)
	require.False(t, d.RunSecondary)
}

func TestPairwiseDecisionSkipsUnresponsiveCandidate(t *testing.T) {
	withPairwisePolicyForTest(t)
	perf := NewPerfTracker(nil)
	for i := 0; i < 4; i++ {
		perf.RecordRequest(pairwiseTestRecord())
	}
	perf.Record(RequestSample{ParticipantKey: "host:1", Responsive: false})

	redundancy := &Redundancy{perf: perf, groupSize: 2}
	d, ok := redundancy.decidePairwiseSpeedup(0, 20_000)

	require.False(t, ok)
	require.False(t, d.RunSecondary)
}

func TestPairwiseDecisionSkipsRecentlyQuarantinedCandidate(t *testing.T) {
	withPairwisePolicyForTest(t)
	perf := NewPerfTracker(nil)
	for i := 0; i < 4; i++ {
		perf.RecordRequest(pairwiseTestRecord())
	}
	limiter := NewParticipantRequestLimiter(10, 10)
	limiter.ObserveTransportFailure("host:1", "/sessions/1/chat/completions", assertAnError{})
	require.True(t, limiter.allow("host:1", time.Now().Add(transportFailureQuarantine+time.Second)))

	redundancy := &Redundancy{perf: perf, groupSize: 2, participantLimiter: limiter}
	d, ok := redundancy.decidePairwiseSpeedup(0, 20_000)

	require.False(t, ok)
	require.False(t, d.RunSecondary)
}

func TestPairwiseDecisionSamplesSparsePairFromPrimary(t *testing.T) {
	withPairwisePolicyForTest(t)
	perf := NewPerfTracker(nil)

	redundancy := &Redundancy{perf: perf, groupSize: 2}
	d, ok := redundancy.decidePairwiseSpeedup(0, 20_000)

	require.True(t, ok)
	require.True(t, d.RunSecondary)
	require.Equal(t, "pairwise_ab_sample", d.Reason)
	require.Equal(t, 1, d.ImmediateAttempts)
}

func TestPairwiseDecisionKeepsFurtherSparseSamplingProbabilistic(t *testing.T) {
	withPairwisePolicyForTest(t)
	PairwiseABSparseSampleRate = 0
	perf := NewPerfTracker(nil)

	redundancy := &Redundancy{perf: perf, groupSize: 3}
	d, ok := redundancy.decidePairwiseSpeedup(0, 20_000)

	require.True(t, ok)
	require.True(t, d.RunSecondary)
	require.Equal(t, "pairwise_ab_sample", d.Reason)
	require.Equal(t, 1, d.ImmediateAttempts)
}

func TestPairwiseDecisionRunsABCForFailedABFollowUp(t *testing.T) {
	withPairwisePolicyForTest(t)
	PairwiseABSparseSampleRate = 0
	perf := NewPerfTracker(nil)
	perf.RecordRequest(pairwiseFailedBRecord())

	redundancy := &Redundancy{perf: perf, groupSize: 3}
	d, ok := redundancy.decidePairwiseSpeedup(0, 20_000)

	require.True(t, ok)
	require.True(t, d.RunSecondary)
	require.Equal(t, "pairwise_ab_sample", d.Reason)
	require.Equal(t, 2, d.ImmediateAttempts)
}

func TestPairwiseDecisionSamplesNextEdgeAfterAcceptedAttempt(t *testing.T) {
	withPairwisePolicyForTest(t)
	PairwiseABSparseSampleRate = 1
	perf := NewPerfTracker(nil)
	for i := 0; i < 4; i++ {
		perf.RecordRequest(pairwiseTestRecord())
	}

	redundancy := &Redundancy{perf: perf, groupSize: 3}
	d, ok := redundancy.decidePairwiseSpeedup(0, 20_000)

	require.True(t, ok)
	require.True(t, d.RunSecondary)
	require.Equal(t, "pairwise_budgeted_speedup", d.Reason)
	require.Equal(t, 2, d.ImmediateAttempts)
}

func pairwiseTestRecord() RequestRecord {
	return RequestRecord{
		Timestamp:     time.Now(),
		Model:         "",
		InputTokens:   20_000,
		WinnerHostIdx: 1,
		WinnerNonce:   2,
		Decision:      "receipt_timeout",
		Hosts: []HostInvolvement{
			{
				HostIdx:        0,
				ParticipantKey: "host:0",
				Nonce:          1,
				TotalTimeMs:    20_000,
				Responsive:     true,
				Finished:       true,
			},
			{
				HostIdx:        1,
				ParticipantKey: "host:1",
				Nonce:          2,
				TotalTimeMs:    10_000,
				Responsive:     true,
				Finished:       true,
				Winner:         true,
			},
		},
	}
}

func pairwiseFailedBRecord() RequestRecord {
	return RequestRecord{
		Timestamp:     time.Now(),
		Model:         "",
		InputTokens:   20_000,
		WinnerHostIdx: 0,
		WinnerNonce:   1,
		Decision:      "pairwise_ab_sample",
		Hosts: []HostInvolvement{
			{
				HostIdx:        0,
				ParticipantKey: "host:0",
				Nonce:          1,
				TotalTimeMs:    10_000,
				Responsive:     true,
				Finished:       true,
				Winner:         true,
			},
			{
				HostIdx:        1,
				ParticipantKey: "host:1",
				Nonce:          2,
				Responsive:     false,
				Finished:       false,
			},
		},
	}
}

type assertAnError struct{}

func (assertAnError) Error() string { return "boom" }
