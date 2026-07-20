package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewChainPhaseGateUsesChainRESTPaths(t *testing.T) {
	gate := NewChainPhaseGate("http://node:1317/", 0)
	require.NotNil(t, gate)
	require.Equal(t, "http://node:1317/productscience/inference/inference/epoch_info", gate.endpoint)
	require.Equal(t, "http://node:1317/productscience/inference/inference/active_participants/0", gate.participantsEndpoint)
	gate.SetPreservedSnapshotBaseURL("http://node:1317/")
	require.Equal(t, "http://node:1317/productscience/inference/inference/preserved_nodes_snapshot", gate.preservedSnapshotURL())
}

func TestChainPhaseGateFetchEpochInfoUsesChainPrecomputedStages(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/productscience/inference/inference/epoch_info", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"block_height":"150",
			"phase":"Inference",
			"effective_epoch_index":"11",
			"latest_epoch":{"index":"12","poc_start_block_height":"100"},
			"epoch_stages":{"epoch_index":"12","set_new_validators":"180","next_poc_start":"200"},
			"next_epoch_stages":{"epoch_index":"13","set_new_validators":"600","next_poc_start":"700"},
			"is_confirmation_poc_active":false
		}`))
	}))
	defer server.Close()

	gate := NewChainPhaseGate(server.URL, 0)
	resp, err := gate.fetchEpochInfo()
	require.NoError(t, err)
	require.Equal(t, epochPhaseInference, resp.Phase)
	require.Equal(t, uint64(11), uint64(resp.EffectiveEpochIndex))
	require.Equal(t, int64(180), int64(resp.EpochStages.SetNewValidators))
	require.Equal(t, int64(600), int64(resp.NextEpochStages.SetNewValidators))

	snapshot := deriveChainPhaseSnapshot(resp)
	require.Equal(t, int64(180), snapshot.epochSwitchBlockHeight)
	require.Equal(t, uint64(12), snapshot.EpochIndex)
}
