package rpcserver

import (
	"testing"

	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"

	"devshard/observability"
)

func metricCounter(t *testing.T, name string, labels map[string]string) float64 {
	t.Helper()
	families, err := observability.Registry().Gather()
	require.NoError(t, err)
	for _, f := range families {
		if f.GetName() != name {
			continue
		}
		for _, m := range f.Metric {
			if metricLabelsMatch(m, labels) && m.Counter != nil {
				return m.Counter.GetValue()
			}
		}
	}
	return 0
}

func metricGauge(t *testing.T, name string) float64 {
	t.Helper()
	families, err := observability.Registry().Gather()
	require.NoError(t, err)
	for _, f := range families {
		if f.GetName() != name {
			continue
		}
		for _, m := range f.Metric {
			if m.Gauge != nil {
				return m.Gauge.GetValue()
			}
		}
	}
	return 0
}

func metricLabelsMatch(m *dto.Metric, want map[string]string) bool {
	if len(want) == 0 {
		return true
	}
	got := make(map[string]string, len(m.Label))
	for _, lp := range m.Label {
		got[lp.GetName()] = lp.GetValue()
	}
	for k, v := range want {
		if got[k] != v {
			return false
		}
	}
	return true
}
