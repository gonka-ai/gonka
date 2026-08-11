package broker

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"decentralized-api/apiconfig"
	"decentralized-api/mlnodeclient"

	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func TestUpdateNodeResultCommand_PreservesDirtyOnDeferredDeployment(t *testing.T) {
	b := NewTestBroker()
	retryAfter := time.Now().Add(time.Minute)
	node := createTestNodeWithStatus("node-1", types.HardwareNodeStatus_INFERENCE)
	node.State.IntendedStatus = types.HardwareNodeStatus_INFERENCE
	node.State.DeploymentUpdatePending = true
	node.State.ReconcileInfo = &ReconcileInfo{
		Status:    types.HardwareNodeStatus_INFERENCE,
		PocStatus: PocStatusIdle,
	}
	b.nodes[node.Node.Id] = node

	command := NewUpdateNodeResultCommand(node.Node.Id, NodeResult{
		Succeeded:            true,
		FinalStatus:          types.HardwareNodeStatus_INFERENCE,
		OriginalTarget:       types.HardwareNodeStatus_INFERENCE,
		FinalPocStatus:       PocStatusIdle,
		OriginalPocTarget:    PocStatusIdle,
		DeploymentDeferred:   true,
		DeploymentRetryAfter: retryAfter,
	})
	command.Execute(b)

	require.True(t, node.State.DeploymentUpdatePending)
	require.Equal(t, retryAfter, node.State.DeploymentRetryAfter)
	require.Nil(t, node.State.ReconcileInfo)
}

func TestUpdateNodeResultCommand_ClearsDirtyAfterAppliedDeployment(t *testing.T) {
	b := NewTestBroker()
	node := createTestNodeWithStatus("node-1", types.HardwareNodeStatus_INFERENCE)
	node.State.IntendedStatus = types.HardwareNodeStatus_INFERENCE
	node.State.DeploymentUpdatePending = true
	node.State.DeploymentRetryAfter = time.Now().Add(time.Minute)
	node.State.ReconcileInfo = &ReconcileInfo{
		Status:    types.HardwareNodeStatus_INFERENCE,
		PocStatus: PocStatusIdle,
	}
	b.nodes[node.Node.Id] = node

	command := NewUpdateNodeResultCommand(node.Node.Id, NodeResult{
		Succeeded:             true,
		FinalStatus:           types.HardwareNodeStatus_INFERENCE,
		OriginalTarget:        types.HardwareNodeStatus_INFERENCE,
		FinalPocStatus:        PocStatusIdle,
		OriginalPocTarget:     PocStatusIdle,
		DeploymentApplied:     true,
		DeploymentFingerprint: "fingerprint",
		DeploymentModelID:     "model1",
	})
	command.Execute(b)

	require.False(t, node.State.DeploymentUpdatePending)
	require.True(t, node.State.DeploymentRetryAfter.IsZero())
}

func TestDeploymentUpdateReadyHonorsRetryBackoff(t *testing.T) {
	node := createTestNodeWithStatus("node-1", types.HardwareNodeStatus_INFERENCE)
	node.State.IntendedStatus = types.HardwareNodeStatus_INFERENCE
	node.State.DeploymentUpdatePending = true
	now := time.Now()

	node.State.DeploymentRetryAfter = now.Add(time.Minute)
	require.False(t, deploymentUpdateReady(node, now))

	node.State.DeploymentRetryAfter = now.Add(-time.Second)
	require.True(t, deploymentUpdateReady(node, now))
}

func TestRefreshDeploymentUpdatePendingFromApplied(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte("api:\n  port: 8080\n"), 0o644))
	manager, err := apiconfig.LoadConfigManagerWithPaths(configPath, filepath.Join(dir, "gonka.db"), "")
	require.NoError(t, err)

	const modelID = "MiniMaxAI/MiniMax-M2.7"
	node := createTestNodeWithStatus("node-1", types.HardwareNodeStatus_INFERENCE)
	node.Node.Models = map[string]ModelArgs{
		modelID: {
			ModelOverride: &apiconfig.ModelOverride{HfRepo: "host/custom-minimax"},
		},
	}
	node.State.EpochModels[modelID] = types.Model{Id: modelID}
	node.State.EpochMLNodes[modelID] = types.MLNodeInfo{NodeId: node.Node.Id}
	b := &Broker{
		nodes:         map[string]*NodeWithState{node.Node.Id: node},
		configManager: manager,
	}

	b.refreshDeploymentUpdatePendingFromApplied(node.Node.Id)
	require.True(t, node.State.DeploymentUpdatePending)

	node.State.DeploymentUpdatePending = false
	deployment := b.ResolveModelDeployment(node.State.EpochModels[modelID], node.Node.Models[modelID])
	require.NoError(t, manager.SetAppliedDeploymentFingerprint(
		context.Background(), node.Node.Id, modelID, deployment.Fingerprint(),
	))
	b.refreshDeploymentUpdatePendingFromApplied(node.Node.Id)
	require.False(t, node.State.DeploymentUpdatePending)
}

func TestNonOverrideDeploymentDeletesStaleAppliedFingerprint(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte("api:\n  port: 8080\n"), 0o644))
	manager, err := apiconfig.LoadConfigManagerWithPaths(configPath, filepath.Join(dir, "gonka.db"), "")
	require.NoError(t, err)

	const modelID = "MiniMaxAI/MiniMax-M2.7"
	node := createTestNodeWithStatus("node-1", types.HardwareNodeStatus_INFERENCE)
	node.State.IntendedStatus = types.HardwareNodeStatus_INFERENCE
	node.State.DeploymentUpdatePending = true
	node.State.ReconcileInfo = &ReconcileInfo{
		Status:    types.HardwareNodeStatus_INFERENCE,
		PocStatus: PocStatusIdle,
	}
	b := NewTestBroker()
	b.configManager = manager
	b.nodes[node.Node.Id] = node
	require.NoError(t, manager.SetAppliedDeploymentFingerprint(
		context.Background(), node.Node.Id, modelID, "old-override",
	))

	command := NewUpdateNodeResultCommand(node.Node.Id, NodeResult{
		Succeeded:         true,
		FinalStatus:       types.HardwareNodeStatus_INFERENCE,
		OriginalTarget:    types.HardwareNodeStatus_INFERENCE,
		FinalPocStatus:    PocStatusIdle,
		OriginalPocTarget: PocStatusIdle,
		DeploymentApplied: true,
		DeploymentModelID: modelID,
	})
	command.Execute(b)

	_, found, err := manager.GetAppliedDeploymentFingerprint(context.Background(), node.Node.Id, modelID)
	require.NoError(t, err)
	require.False(t, found)
	require.False(t, node.State.DeploymentUpdatePending)
}

func TestRemovedOverrideWithStaleFingerprintMarksNodeDirty(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte("api:\n  port: 8080\n"), 0o644))
	manager, err := apiconfig.LoadConfigManagerWithPaths(configPath, filepath.Join(dir, "gonka.db"), "")
	require.NoError(t, err)

	const modelID = "MiniMaxAI/MiniMax-M2.7"
	node := createTestNodeWithStatus("node-1", types.HardwareNodeStatus_INFERENCE)
	node.Node.Models = map[string]ModelArgs{modelID: {}}
	node.State.EpochModels[modelID] = types.Model{Id: modelID}
	node.State.EpochMLNodes[modelID] = types.MLNodeInfo{NodeId: node.Node.Id}
	b := &Broker{
		nodes:         map[string]*NodeWithState{node.Node.Id: node},
		configManager: manager,
	}
	require.NoError(t, manager.SetAppliedDeploymentFingerprint(
		context.Background(), node.Node.Id, modelID, "old-override",
	))

	b.refreshDeploymentUpdatePendingFromApplied(node.Node.Id)

	require.True(t, node.State.DeploymentUpdatePending)
}

func TestQueryNodeStatusUsesOverrideWhenEpochModelIsMissing(t *testing.T) {
	const modelID = "MiniMaxAI/MiniMax-M2.7"
	b := NewTestBroker()
	node := Node{
		Id:               "node-1",
		Host:             "mlnode",
		InferencePort:    5000,
		PoCPort:          8080,
		InferenceSegment: "/inference",
		PoCSegment:       "/poc",
		Models: map[string]ModelArgs{
			modelID: {
				ModelOverride: &apiconfig.ModelOverride{HfRepo: "host/custom-minimax"},
			},
		},
	}
	state := NodeState{
		CurrentStatus: types.HardwareNodeStatus_INFERENCE,
		EpochMLNodes: map[string]types.MLNodeInfo{
			modelID: {NodeId: node.Id},
		},
		EpochModels: map[string]types.Model{},
	}
	factory := b.mlNodeClientFactory.(*mlnodeclient.MockClientFactory)
	client := factory.CreateClient(node.PoCUrl(), node.InferenceUrl()).(*mlnodeclient.MockClient)
	client.CurrentState = mlnodeclient.MlNodeState_INFERENCE
	client.InferenceIsHealthy = true
	client.LastInferenceModel = "host/custom-minimax"
	client.LastInferenceArgs = []string{"--served-model-name", modelID}

	result, err := b.queryNodeStatus(node, state)

	require.NoError(t, err)
	require.Equal(t, types.HardwareNodeStatus_INFERENCE, result.CurrentStatus)
}

func TestQueryNodeStatusKeepsHealthyOldDeploymentWhileOverrideIsDirty(t *testing.T) {
	const modelID = "MiniMaxAI/MiniMax-M2.7"
	b := NewTestBroker()
	node := Node{
		Id:               "node-1",
		Host:             "mlnode",
		InferencePort:    5000,
		PoCPort:          8080,
		InferenceSegment: "/inference",
		PoCSegment:       "/poc",
		Models: map[string]ModelArgs{
			modelID: {
				ModelOverride: &apiconfig.ModelOverride{HfRepo: "host/new-minimax"},
			},
		},
	}
	state := NodeState{
		CurrentStatus:           types.HardwareNodeStatus_INFERENCE,
		DeploymentUpdatePending: true,
		EpochMLNodes: map[string]types.MLNodeInfo{
			modelID: {NodeId: node.Id},
		},
		EpochModels: map[string]types.Model{
			modelID: {Id: modelID},
		},
	}
	factory := b.mlNodeClientFactory.(*mlnodeclient.MockClientFactory)
	client := factory.CreateClient(node.PoCUrl(), node.InferenceUrl()).(*mlnodeclient.MockClient)
	client.CurrentState = mlnodeclient.MlNodeState_INFERENCE
	client.InferenceIsHealthy = true
	client.LastInferenceModel = "host/old-minimax"
	client.LastInferenceArgs = []string{"--served-model-name", modelID}

	result, err := b.queryNodeStatus(node, state)

	require.NoError(t, err)
	require.Equal(t, types.HardwareNodeStatus_INFERENCE, result.CurrentStatus)
}

func TestShouldCancelForDeploymentChangeOnlyCancelsInference(t *testing.T) {
	cancel := func() {}

	require.True(t, shouldCancelForDeploymentChange(NodeState{
		cancelInFlightTask: cancel,
		ReconcileInfo: &ReconcileInfo{
			Status: types.HardwareNodeStatus_INFERENCE,
		},
	}))
	require.False(t, shouldCancelForDeploymentChange(NodeState{
		cancelInFlightTask: cancel,
		ReconcileInfo: &ReconcileInfo{
			Status: types.HardwareNodeStatus_POC,
		},
	}))
	require.False(t, shouldCancelForDeploymentChange(NodeState{
		cancelInFlightTask: cancel,
	}))
}
