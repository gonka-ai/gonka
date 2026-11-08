package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

// Test flow:
//  1. Build a race recorder via `newTestRaceRecorder`.
//  2. Record two sweeps with different applied and failed counts via `RecordSweep`.
//  3. Assert only the applied and failed outcomes are published, and their totals sum across the two sweeps.
func TestSweepCountsWhatEachTickAppliedAndFailed(t *testing.T) {
	recorder := newTestRaceRecorder(New())

	recorder.RecordSweep(5, 3, 2)
	recorder.RecordSweep(1, 1, 0)

	require.Equal(t, 2, testutil.CollectAndCount(recorder.sweeps), "only applied and failed are published")
	require.Equal(t, float64(4), testutil.ToFloat64(recorder.sweeps.WithLabelValues(sweepOutcomeApplied)))
	require.Equal(t, float64(2), testutil.ToFloat64(recorder.sweeps.WithLabelValues(sweepOutcomeFailed)))
}

// Test flow:
//  1. Build a race recorder via `newTestRaceRecorder`.
//  2. Record a sweep with zero due, applied, and failed.
//  3. Assert nothing was published.
func TestSweepWithNothingDueCountsNothing(t *testing.T) {
	recorder := newTestRaceRecorder(New())

	recorder.RecordSweep(0, 0, 0)

	require.Zero(t, testutil.CollectAndCount(recorder.sweeps))
}
