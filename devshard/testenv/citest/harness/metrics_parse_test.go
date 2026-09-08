package harness

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseMetricsText(t *testing.T) {
	body := `
# HELP demo
demo_up{host="http://h:1"} 1
demo_targets 3
demo_hist_count 7
`
	metrics := ParseMetricsText(body)
	m, ok := FindMetric(metrics, "demo_up", map[string]string{"host": "http://h:1"})
	require.True(t, ok)
	require.Equal(t, 1.0, m.Value)
	m, ok = FindMetric(metrics, "demo_targets", nil)
	require.True(t, ok)
	require.Equal(t, 3.0, m.Value)
	require.Equal(t, 7.0, HistogramSampleCount(metrics, "demo_hist"))
}
