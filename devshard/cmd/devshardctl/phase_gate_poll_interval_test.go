package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestNewChainPhaseGateDefaultsStayUnderProxyAPIZoneLimit(t *testing.T) {
	t.Setenv(chainPhasePollEnvInterval, "")
	t.Setenv(versionsPollEnvInterval, "")

	gate := NewChainPhaseGate("http://api:9000", 0)
	require.Equal(t, 15*time.Second, gate.pollInterval)
	require.Equal(t, 60*time.Second, gate.versionsPollInterval)
	require.Equal(t, versionsTTLPollMultiplier*60*time.Second, gate.versions.ttl)

	// Worst-case steady-state rate on a PublicAPI node that is also a candidate miner:
	// 2 phase-gate requests (/v1/epochs/latest + /v1/epochs/current/participants) per
	// pollInterval plus 1 /v1/versions request per versionsPollInterval must stay < 10/min.
	reqsPerMin := 2.0*(float64(time.Minute)/float64(gate.pollInterval)) +
		float64(time.Minute)/float64(gate.versionsPollInterval)
	require.Less(t, reqsPerMin, 10.0)
}

func TestNewChainPhaseGateRespectsPollIntervalEnvOverrides(t *testing.T) {
	t.Setenv(chainPhasePollEnvInterval, "20s")
	t.Setenv(versionsPollEnvInterval, "45s")

	gate := NewChainPhaseGate("http://api:9000", 0)
	require.Equal(t, 20*time.Second, gate.pollInterval)
	require.Equal(t, 45*time.Second, gate.versionsPollInterval)
	require.Equal(t, versionsTTLPollMultiplier*45*time.Second, gate.versions.ttl)
}

func TestNewChainPhaseGateInvalidEnvFallsBackToDefaults(t *testing.T) {
	t.Setenv(chainPhasePollEnvInterval, "not-a-duration")
	t.Setenv(versionsPollEnvInterval, "-5s")

	gate := NewChainPhaseGate("http://api:9000", 0)
	require.Equal(t, defaultChainPhasePollInterval, gate.pollInterval)
	require.Equal(t, defaultVersionsPollInterval, gate.versionsPollInterval)
}

func TestVersionsCacheWarmsNewCandidatesWithoutWaitingForSlowTicker(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/versions" {
			http.Error(w, "bad path", http.StatusNotFound)
			return
		}
		hits.Add(1)
		_, _ = w.Write([]byte(`{"mlnodes":[{"node_id":"n1","poc_validation_inference":true}]}`))
	}))
	defer srv.Close()

	cache := NewVersionsCache(&http.Client{Timeout: time.Second}, 3*time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go cache.Run(ctx, time.Hour)

	cache.SetCandidates(map[string]string{"miner-1": srv.URL})
	require.Eventually(t, func() bool {
		return cache.IsNodeValidationCapable("miner-1", "n1")
	}, time.Second, 10*time.Millisecond)

	// Re-applying the same already-polled candidate set must not trigger another fetch.
	cache.SetCandidates(map[string]string{"miner-1": srv.URL})
	time.Sleep(50 * time.Millisecond)
	require.Equal(t, int64(1), hits.Load())
}
