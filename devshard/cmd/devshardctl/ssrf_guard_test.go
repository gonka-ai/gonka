package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"common/httpguard"

	"github.com/stretchr/testify/require"
)

// Tests in this package serve hosts from httptest on loopback, which the
// dial-time SSRF guard rejects; production leaves it on
// (DEVSHARD_ALLOW_PRIVATE_ADDRESSES).
func TestMain(m *testing.M) {
	httpguard.SetAllowPrivate(true)
	os.Exit(m.Run())
}

// privateHost serves 200 on loopback and counts hits. It returns the base by
// IP and by the host name "localhost", which the registration gate's
// literal-IP check alone would not stop.
func privateHost(t *testing.T) (*atomic.Int32, []string) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)
	httpguard.SetAllowPrivate(false)
	t.Cleanup(func() { httpguard.SetAllowPrivate(true) })
	byName := "http://localhost:" + strings.TrimPrefix(srv.URL, "http://127.0.0.1:")
	return &hits, []string{srv.URL, byName}
}

// GET <InferenceUrl>/v1/versions goes to every active participant's on-chain
// URL and must not reach a private address.
func TestVersionsCache_DoesNotDialPrivateHost(t *testing.T) {
	hits, bases := privateHost(t)
	gate := NewChainPhaseGate("", time.Second)
	for _, base := range bases {
		gate.versions.fetchOne(context.Background(), base)
	}
	require.Equal(t, int32(0), hits.Load())
}

// The host-ping prober probes each escrow host's on-chain base URL and must
// not reach a private address.
func TestHostPing_DoesNotDialPrivateHost(t *testing.T) {
	hits, bases := privateHost(t)
	metrics := NewDevshardMetrics()
	job := newHostPingJob(metrics, hostPingConfig{
		Interval:    200 * time.Millisecond, // probe.New wants Interval >= 2*Timeout
		Timeout:     50 * time.Millisecond,
		Concurrency: 2,
	})
	for i, base := range bases {
		job.ObserveEscrowHost("e1", base, "/devshard/v4", "pk-"+string(rune('a'+i)))
	}
	job.start()
	defer job.stop()
	require.Eventually(t, func() bool {
		families, err := metrics.registry.Gather()
		if err != nil {
			return false
		}
		return metricCounterValueOrZero(families, "devshard_gateway_host_ping_ticks_total", nil) >= 3
	}, 3*time.Second, 20*time.Millisecond)
	require.Equal(t, int32(0), hits.Load())
}
