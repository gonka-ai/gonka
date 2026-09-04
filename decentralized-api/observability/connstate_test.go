package observability

import (
	"net"
	"net/http"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

func TestConnState_TracksTransitions(t *testing.T) {
	hook := ConnState("ml-test")
	c1, c2 := net.Pipe()
	t.Cleanup(func() {
		_ = c1.Close()
		_ = c2.Close()
	})

	hook(c1, http.StateNew)
	require.Equal(t, 1.0, testutil.ToFloat64(httpConnections.WithLabelValues("ml-test", "new")))

	hook(c1, http.StateActive)
	require.Equal(t, 0.0, testutil.ToFloat64(httpConnections.WithLabelValues("ml-test", "new")))
	require.Equal(t, 1.0, testutil.ToFloat64(httpConnections.WithLabelValues("ml-test", "active")))

	hook(c1, http.StateClosed)
	require.Equal(t, 0.0, testutil.ToFloat64(httpConnections.WithLabelValues("ml-test", "active")))

	families, err := prometheus.DefaultGatherer.Gather()
	require.NoError(t, err)
	var found bool
	for _, f := range families {
		if f.GetName() == "decentralized_api_http_connections_total" {
			found = true
			break
		}
	}
	require.True(t, found, "connections_total should be registered on the default gatherer")
}
