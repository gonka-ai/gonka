package mlnode

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"decentralized-api/apiconfig"
	"decentralized-api/broker"
	"decentralized-api/mlnodeclient"
	"decentralized-api/poc/artifacts"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func TestModelNodeCallbackPreservesWeightAttribution(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte("{}"), 0600))
	config, err := apiconfig.LoadConfigManagerWithPaths(path, filepath.Join(dir, "config.db"), "")
	require.NoError(t, err)
	defer config.SqlDb().GetDb().Close()
	require.NoError(t, apiconfig.KVSetJSON(context.Background(), config.SqlDb().GetDb(), "poc_node_numbers", map[string]any{
		"height": 100, "models": map[string][]string{"model-a": {"original-a"}, "model-b": {"original-b"}},
	}))
	tracker := newMLNodeTestBroker(t, types.PoCGeneratePhase, testModelA).GetPhaseTracker()
	b := broker.NewBroker(stubBrokerChainBridge{models: []string{"model-a", "model-b"}}, tracker, nil, "", mlnodeclient.NewMockClientFactory(), config)
	store := artifacts.NewManagedArtifactStore(t.TempDir(), 3)
	defer store.Close()
	server := NewServer(nil, b, WithArtifactStore(store))
	for _, model := range []string{"model-a", "model-b"} {
		body := []byte(`{"block_height":100,"node_id":0,"artifacts":[{"nonce":0,"vector_b64":"AQID"}]}`)
		req := httptest.NewRequest(http.MethodPost, "/v2/poc-batches/"+model+"/m/generated", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		server.e.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		modelStore, err := store.GetOrCreateStore(100, model)
		require.NoError(t, err)
		require.Equal(t, map[string]uint32{"original-" + model[len("model-"):]: 1}, modelStore.GetNodeCounts())
	}
}

func TestModelNodeDecodeCallbackPreservesWeightAttribution(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte("{}"), 0600))
	config, err := apiconfig.LoadConfigManagerWithPaths(path, filepath.Join(dir, "config.db"), "")
	require.NoError(t, err)
	defer config.SqlDb().GetDb().Close()
	require.NoError(t, apiconfig.KVSetJSON(context.Background(), config.SqlDb().GetDb(), "poc_node_numbers", map[string]any{
		"height": 100, "models": map[string][]string{"model-a": {"original-a"}},
	}))
	bridge := stubBrokerChainBridge{
		models: []string{"model-a"}, scheme: types.PocScheme_POC_SCHEME_DECODE, seqLen: 256, decodeSteps: 2,
	}
	tracker := newMLNodeTestBrokerWithBridge(t, types.PoCGeneratePhase, bridge).GetPhaseTracker()
	b := broker.NewBroker(bridge, tracker, nil, "", mlnodeclient.NewMockClientFactory(), config)
	store := artifacts.NewManagedArtifactStore(t.TempDir(), 3)
	defer store.Close()
	server := NewServer(nil, b, WithArtifactStore(store))

	body := []byte(`{"block_height":100,"node_id":0,"artifacts":[{"nonce":0,"k_points_steps":[1,2,3]}]}`)
	req := httptest.NewRequest(http.MethodPost, "/v3/poc-batches/model-a/m/generated", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	modelStore, err := store.GetOrCreateStore(100, "model-a")
	require.NoError(t, err)
	require.Equal(t, map[string]uint32{"original-a": 1}, modelStore.GetNodeCounts())
}
