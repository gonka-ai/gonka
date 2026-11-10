package observability_test

import (
	"testing"
	"time"

	"common/probe"

	"decentralized-api/observability"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"
)

func TestObserveMLNodePingResultWarmHistogram(t *testing.T) {
	observability.ObserveMLNodePingResult(probe.Result{
		Key:        "n-warm",
		Up:         true,
		Kind:       probe.KindDate,
		RTT:        5 * time.Millisecond,
		ConnReused: true,
		At:         time.Now(),
	})
	observability.ObserveMLNodePingResult(probe.Result{
		Key:        "n-warm",
		Up:         true,
		Kind:       probe.KindDate,
		RTT:        5 * time.Millisecond,
		ConnReused: false,
		At:         time.Now(),
	})
	require.GreaterOrEqual(t, histogramCount(t, "dapi_mlnode_ping_warm_rtt_seconds"), uint64(1))
	observability.DeleteMLNodePingMetrics("n-warm")
}

func histogramCount(t *testing.T, name string) uint64 {
	t.Helper()
	mfs, err := prometheus.DefaultGatherer.Gather()
	require.NoError(t, err)
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
		for _, m := range mf.GetMetric() {
			if m.Histogram != nil {
				return m.Histogram.GetSampleCount()
			}
		}
	}
	return 0
}

var _ = dto.Metric{}
