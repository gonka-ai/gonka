package harness

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMetricLineValue(t *testing.T) {
	body := `# HELP demo
devshard_gateway_heightsync_cadence_events_total{devshard_id="12",event="heartbeat_opened"} 3
devshard_gateway_heightsync_cadence_events_total{devshard_id="12",event="discharged_by_inference"} 7
devshard_gateway_heightsync_height_spread{devshard_id="12"} 5
`
	v, ok := MetricLineValue(body, "devshard_gateway_heightsync_cadence_events_total",
		map[string]string{"event": "discharged_by_inference"})
	require.True(t, ok)
	require.Equal(t, 7.0, v)

	spread, ok := MetricLineValue(body, "devshard_gateway_heightsync_height_spread", nil)
	require.True(t, ok)
	require.Equal(t, 5.0, spread)

	_, ok = MetricLineValue(body, "devshard_gateway_heightsync_peer_seen", nil)
	require.False(t, ok)
}

func TestAnyMetricHasLabel(t *testing.T) {
	body := `devshard_gateway_heightsync_host_height{devshard_id="99",slot="0"} 100
`
	require.True(t, AnyMetricHasLabel(body, "devshard_id", "99"))
	require.False(t, AnyMetricHasLabel(body, "devshard_id", "12"))
}
