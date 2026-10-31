package broker

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"decentralized-api/apiconfig"
	"decentralized-api/chainphase"
	"decentralized-api/mlnodeclient"
	"github.com/productscience/inference/x/inference/types"
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
	b.pocNodeNumbers = &pocNodeNumbers{Rollout: &pocNodeNumberRollout{Ready: true}}
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

func TestPoCNodeNumbers_RolloutDuringGenerationDefersUntilNextStage(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte("{}"), 0600))
	config, err := apiconfig.LoadConfigManagerWithPaths(configPath, filepath.Join(dir, "config.db"), "")
	require.NoError(t, err)
	defer config.SqlDb().GetDb().Close()

	require.NoError(t, apiconfig.KVSetJSON(ctx, config.SqlDb().GetDb(), pocNodeNumbersKey, &pocNodeNumbers{
		Height: 100,
		Models: map[string][]string{"model-a": {"node-1"}},
	}))
	nodes := map[string]*NodeWithState{
		"node-1": {Node: Node{Id: "node-1", Models: map[string]ModelArgs{"model-a": {}}}},
	}
	b := &Broker{configManager: config, nodes: nodes}
	epochState := &chainphase.EpochState{
		LatestEpoch:  types.NewEpochContext(types.Epoch{Index: 1, PocStartBlockHeight: 100}, types.EpochParams{}),
		CurrentBlock: chainphase.BlockInfo{Height: 110},
		CurrentPhase: types.PoCGeneratePhase,
		IsSynced:     true,
	}
	require.NoError(t, b.preparePoCNodeNumberingRollout(ctx, epochState))

	// Keep the post2 mapping available for callbacks already in flight, but do
	// not dispatch either global or local starts into an ambiguous active stage.
	original, err := b.PoCNodeID(ctx, 100, "model-a", 0)
	require.NoError(t, err)
	require.Equal(t, "node-1", original)
	params := &pocParams{startPoCBlockHeight: 100, models: map[string]apiconfig.PoCModelConfigCache{
		"model-a": {ModelId: "model-a"},
	}}
	cmd := StartPoCNodeCommandV2{Model: "model-a"}
	err = b.assignPoCNodeNumber(ctx, "node-1", params, &cmd)
	require.ErrorIs(t, err, errPoCNodeNumberRolloutStage)

	// The deferral is durable across restart. The next stage atomically enables
	// model-local numbering and replaces the old callback mapping.
	restarted := &Broker{configManager: config, nodes: nodes}
	err = restarted.assignPoCNodeNumber(ctx, "node-1", params, &cmd)
	require.ErrorIs(t, err, errPoCNodeNumberRolloutStage)
	params.startPoCBlockHeight = 101
	require.NoError(t, restarted.assignPoCNodeNumber(ctx, "node-1", params, &cmd))
	require.Equal(t, 0, *cmd.ModelNodeID)
	require.Equal(t, 1, cmd.TotalNodes)
}

func TestPoCNodeNumbers_RolloutBetweenStagesEnablesImmediately(t *testing.T) {
	b := &Broker{nodes: map[string]*NodeWithState{
		"node-1": {Node: Node{Id: "node-1", Models: map[string]ModelArgs{"model-a": {}}}},
	}}
	epochState := &chainphase.EpochState{
		LatestEpoch:  types.NewEpochContext(types.Epoch{Index: 1, PocStartBlockHeight: 100}, types.EpochParams{}),
		CurrentBlock: chainphase.BlockInfo{Height: 300},
		CurrentPhase: types.InferencePhase,
		IsSynced:     true,
	}
	require.NoError(t, b.preparePoCNodeNumberingRollout(context.Background(), epochState))
	params := &pocParams{startPoCBlockHeight: 400, models: map[string]apiconfig.PoCModelConfigCache{
		"model-a": {ModelId: "model-a"},
	}}
	cmd := StartPoCNodeCommandV2{Model: "model-a"}
	require.NoError(t, b.assignPoCNodeNumber(context.Background(), "node-1", params, &cmd))
	require.Equal(t, 0, *cmd.ModelNodeID)
}

func TestPoCNodeNumbers_RolloutDuringGenerationWithoutCurrentMappingStaysGlobal(t *testing.T) {
	b := &Broker{nodes: map[string]*NodeWithState{
		"node-1": {Node: Node{Id: "node-1", NodeNum: 7, Models: map[string]ModelArgs{"model-a": {}}}},
	}}
	epochState := &chainphase.EpochState{
		LatestEpoch:  types.NewEpochContext(types.Epoch{Index: 1, PocStartBlockHeight: 100}, types.EpochParams{}),
		CurrentBlock: chainphase.BlockInfo{Height: 110},
		CurrentPhase: types.PoCGeneratePhase,
		IsSynced:     true,
	}
	require.NoError(t, b.preparePoCNodeNumberingRollout(context.Background(), epochState))
	params := &pocParams{startPoCBlockHeight: 100, models: map[string]apiconfig.PoCModelConfigCache{
		"model-a": {ModelId: "model-a"},
	}}
	cmd := StartPoCNodeCommandV2{Model: "model-a", TotalNodes: 8}
	require.NoError(t, b.assignPoCNodeNumber(context.Background(), "node-1", params, &cmd))
	require.Nil(t, cmd.ModelNodeID)
	require.Equal(t, 8, cmd.TotalNodes)
}

func TestPoCNodeNumbers_EpochModelFilterDoesNotDeadlock(t *testing.T) {
	b := &Broker{
		nodes: map[string]*NodeWithState{
			"node-1": {Node: Node{Id: "node-1", Models: map[string]ModelArgs{"model-a": {}}}},
		},
		ownEpochModels: map[string]bool{"model-a": true},
		pocNodeNumbers: &pocNodeNumbers{
			Height:  99,
			Models:  map[string][]string{"model-a": {"node-1"}},
			Rollout: &pocNodeNumberRollout{Ready: true},
		},
	}
	params := &pocParams{
		startPoCBlockHeight: 100,
		epochModelsOnly:     true,
		models: map[string]apiconfig.PoCModelConfigCache{
			"model-a": {ModelId: "model-a"},
		},
	}
	cmd := StartPoCNodeCommandV2{Model: "model-a"}
	err := b.assignPoCNodeNumber(context.Background(), "node-1", params, &cmd)
	require.NoError(t, err)
	require.Equal(t, 0, *cmd.ModelNodeID)
}

func TestActivePoCGenerationHeight_ConfirmationAndChallenge(t *testing.T) {
	epochParams := types.EpochParams{
		PocStageDuration:    100,
		PocExchangeDuration: 50,
	}
	state := &chainphase.EpochState{
		LatestEpoch:  types.NewEpochContext(types.Epoch{Index: 1, PocStartBlockHeight: 100}, epochParams),
		CurrentBlock: chainphase.BlockInfo{Height: 500},
		CurrentPhase: types.InferencePhase,
		IsSynced:     true,
		ActiveConfirmationPoCEvent: &types.ConfirmationPoCEvent{
			TriggerHeight:         440,
			GenerationStartHeight: 450,
			Phase:                 types.ConfirmationPoCPhase_CONFIRMATION_POC_GENERATION,
		},
	}
	height, active := activePoCGenerationHeight(state)
	require.True(t, active)
	require.Equal(t, int64(440), height)

	state.ActiveConfirmationPoCEvent = nil
	withOverlay(t, stubChallengeOverlay{
		self: "me",
		ch:   testOpenCh("me", 777, 900, true, 1),
	})
	height, active = activePoCGenerationHeight(state)
	require.True(t, active)
	require.Equal(t, int64(777), height)
}
