package broker

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"decentralized-api/apiconfig"
	"decentralized-api/mlnodeclient"
	"github.com/stretchr/testify/require"
)

func TestPoCNodeNumbers_PerModelAndRestart(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte("{}"), 0600))
	config, err := apiconfig.LoadConfigManagerWithPaths(configPath, filepath.Join(dir, "config.db"), "")
	require.NoError(t, err)
	defer config.SqlDb().GetDb().Close()
	b := &Broker{configManager: config, nodes: make(map[string]*NodeWithState)}
	for i := 0; i < 151; i++ {
		model := "model-a"
		if i == 150 {
			model = "model-b"
		}
		id := fmt.Sprintf("node-%03d", i)
		b.nodes[id] = &NodeWithState{Node: Node{Id: id, NodeNum: uint64(i + 1), Models: map[string]ModelArgs{model: {}}}}
	}
	params := &pocParams{startPoCBlockHeight: 100, models: map[string]apiconfig.PoCModelConfigCache{
		"model-a": {ModelId: "model-a"}, "model-b": {ModelId: "model-b"},
	}}
	cmd := StartPoCNodeCommandV2{Model: "model-b", BlockHeight: 100, CallbackUrl: "http://api/v2/poc-batches"}
	params.startPoCBlockHeight = 99
	require.NoError(t, b.assignPoCNodeNumber(ctx, "node-150", params, &cmd))
	require.Nil(t, cmd.ModelNodeID, "do not mix nonce strides during hotfix rollout")
	b.pocNodeNumbers = nil // The migration stage also survives restarts.
	require.NoError(t, b.assignPoCNodeNumber(ctx, "node-150", params, &cmd))
	require.Nil(t, cmd.ModelNodeID)
	params.startPoCBlockHeight = 100
	require.NoError(t, b.assignPoCNodeNumber(ctx, "node-150", params, &cmd))
	require.Equal(t, 0, *cmd.ModelNodeID)
	require.Equal(t, 1, cmd.TotalNodes)
	for i := 0; i < 150; i++ {
		a := StartPoCNodeCommandV2{Model: "model-a"}
		id := fmt.Sprintf("node-%03d", i)
		require.NoError(t, b.assignPoCNodeNumber(ctx, id, params, &a))
		require.Equal(t, i, *a.ModelNodeID)
		require.Equal(t, 150, a.TotalNodes)
		original, err := b.PoCNodeID(ctx, 100, "model-a", i)
		require.NoError(t, err)
		require.Equal(t, id, original)
	}
	client := mlnodeclient.NewMockClient()
	worker := NewNodeWorkerWithClient("node-150", b.nodes["node-150"], client, b)
	defer worker.Shutdown()
	require.True(t, cmd.Execute(ctx, worker).Succeeded)
	require.Equal(t, 0, client.LastInitGenerateV2Req.NodeId)
	require.Equal(t, 1, client.LastInitGenerateV2Req.NodeCount)
	require.Equal(t, "http://api/v2/poc-batches/model-b/m", client.LastInitGenerateV2Req.URL)

	// A restarted API loads the same IDs even if its node list changed.
	restarted := &Broker{configManager: config, nodes: b.nodes}
	delete(restarted.nodes, "node-000")
	original, err := restarted.PoCNodeID(ctx, 100, "model-b", 0)
	require.NoError(t, err)
	require.Equal(t, "node-150", original)
	a := StartPoCNodeCommandV2{Model: "model-a"}
	require.NoError(t, restarted.assignPoCNodeNumber(ctx, "node-001", params, &a))
	require.Equal(t, 1, *a.ModelNodeID)
	require.Equal(t, 150, a.TotalNodes)
	for _, index := range []int{-1, 1} {
		_, err = restarted.PoCNodeID(ctx, 100, "model-b", index)
		require.Error(t, err)
	}
	_, err = restarted.PoCNodeID(ctx, 99, "model-b", 0)
	require.Error(t, err)
	params.startPoCBlockHeight++
	require.NoError(t, restarted.assignPoCNodeNumber(ctx, "node-001", params, &a))
	require.Equal(t, 0, *a.ModelNodeID)
	require.Equal(t, 149, a.TotalNodes)
}
