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

func TestUpdateNodeResultCommand_RejectsStaleDeploymentGeneration(t *testing.T) {
	manager := testDeploymentConfigManager(t)
	node := createTestNodeWithStatus("node-1", types.HardwareNodeStatus_INFERENCE)
	node.State.IntendedStatus = types.HardwareNodeStatus_INFERENCE
	node.State.DeploymentUpdatePending = true
	node.State.DeploymentGeneration = 2
	node.State.ReconcileInfo = &ReconcileInfo{
		Status:     types.HardwareNodeStatus_INFERENCE,
		PocStatus:  PocStatusIdle,
		Generation: 2,
	}
	b := NewTestBroker()
	b.configManager = manager
	b.nodes[node.Node.Id] = node

	command := NewUpdateNodeResultCommand(node.Node.Id, NodeResult{
		Succeeded:             true,
		FinalStatus:           types.HardwareNodeStatus_INFERENCE,
		OriginalTarget:        types.HardwareNodeStatus_INFERENCE,
		FinalPocStatus:        PocStatusIdle,
		OriginalPocTarget:     PocStatusIdle,
		DeploymentApplied:     true,
		DeploymentModelID:     "model-a",
		DeploymentFingerprint: "fingerprint-a",
		DeploymentGeneration:  1,
	})
	command.Execute(b)

	require.True(t, node.State.DeploymentUpdatePending)
	require.NotNil(t, node.State.ReconcileInfo)
	require.Equal(t, uint64(2), node.State.ReconcileInfo.Generation)

	_, found, err := manager.GetAppliedDeployment(context.Background(), node.Node.Id)
	require.NoError(t, err)
	require.False(t, found)
}

func TestUpdateNodeResultCommand_AcceptsMatchingDeploymentGeneration(t *testing.T) {
	manager := testDeploymentConfigManager(t)
	node := createTestNodeWithStatus("node-1", types.HardwareNodeStatus_INFERENCE)
	node.State.IntendedStatus = types.HardwareNodeStatus_INFERENCE
	node.State.DeploymentUpdatePending = true
	node.State.DeploymentGeneration = 2
	node.State.ReconcileInfo = &ReconcileInfo{
		Status:     types.HardwareNodeStatus_INFERENCE,
		PocStatus:  PocStatusIdle,
		Generation: 2,
	}
	b := NewTestBroker()
	b.configManager = manager
	b.nodes[node.Node.Id] = node

	command := NewUpdateNodeResultCommand(node.Node.Id, NodeResult{
		Succeeded:             true,
		FinalStatus:           types.HardwareNodeStatus_INFERENCE,
		OriginalTarget:        types.HardwareNodeStatus_INFERENCE,
		FinalPocStatus:        PocStatusIdle,
		OriginalPocTarget:     PocStatusIdle,
		DeploymentApplied:     true,
		DeploymentModelID:     "model-b",
		DeploymentFingerprint: "fingerprint-b",
		DeploymentGeneration:  2,
	})
	command.Execute(b)

	require.False(t, node.State.DeploymentUpdatePending)
	require.Nil(t, node.State.ReconcileInfo)

	got, found, err := manager.GetAppliedDeployment(context.Background(), node.Node.Id)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "model-b", got.ModelID)
	require.Equal(t, "fingerprint-b", got.Fingerprint)
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

func testDeploymentConfigManager(t *testing.T) *apiconfig.ConfigManager {
	t.Helper()
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte("api:\n  port: 8080\n"), 0o644))
	manager, err := apiconfig.LoadConfigManagerWithPaths(configPath, filepath.Join(dir, "gonka.db"), "")
	require.NoError(t, err)
	return manager
}

func TestRefreshDeploymentUpdatePendingFromApplied(t *testing.T) {
	manager := testDeploymentConfigManager(t)

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
	require.NoError(t, manager.SetAppliedDeployment(
		context.Background(), node.Node.Id, apiconfig.AppliedDeploymentState{
			ModelID:     modelID,
			Fingerprint: deployment.Fingerprint(),
		},
	))
	b.refreshDeploymentUpdatePendingFromApplied(node.Node.Id)
	require.False(t, node.State.DeploymentUpdatePending)
}

func TestRefreshMarksDefaultDeploymentDirtyWhenUnrecorded(t *testing.T) {
	manager := testDeploymentConfigManager(t)

	const modelID = "MiniMaxAI/MiniMax-M2.7"
	node := createTestNodeWithStatus("node-1", types.HardwareNodeStatus_INFERENCE)
	node.Node.Models = map[string]ModelArgs{modelID: {}}
	node.State.EpochModels[modelID] = types.Model{Id: modelID}
	node.State.EpochMLNodes[modelID] = types.MLNodeInfo{NodeId: node.Node.Id}
	b := &Broker{
		nodes:         map[string]*NodeWithState{node.Node.Id: node},
		configManager: manager,
	}

	b.refreshDeploymentUpdatePendingFromApplied(node.Node.Id)
	require.True(t, node.State.DeploymentUpdatePending)
}

func TestRefreshMarksDirtyWhenPreviousModelReturns(t *testing.T) {
	manager := testDeploymentConfigManager(t)

	const modelA = "model-a"
	const modelB = "model-b"
	node := createTestNodeWithStatus("node-1", types.HardwareNodeStatus_INFERENCE)
	node.Node.Models = map[string]ModelArgs{
		modelA: {ModelOverride: &apiconfig.ModelOverride{HfRepo: "host/model-a"}},
		modelB: {ModelOverride: &apiconfig.ModelOverride{HfRepo: "host/model-b"}},
	}
	b := &Broker{
		nodes:         map[string]*NodeWithState{node.Node.Id: node},
		configManager: manager,
	}

	node.State.EpochModels[modelA] = types.Model{Id: modelA}
	node.State.EpochMLNodes[modelA] = types.MLNodeInfo{NodeId: node.Node.Id}
	appliedA := b.ResolveModelDeployment(node.State.EpochModels[modelA], node.Node.Models[modelA])
	require.NoError(t, manager.SetAppliedDeployment(context.Background(), node.Node.Id, apiconfig.AppliedDeploymentState{
		ModelID:     modelA,
		Fingerprint: appliedA.Fingerprint(),
	}))

	node.State.EpochModels = map[string]types.Model{modelB: {Id: modelB}}
	node.State.EpochMLNodes = map[string]types.MLNodeInfo{modelB: {NodeId: node.Node.Id}}
	appliedB := b.ResolveModelDeployment(node.State.EpochModels[modelB], node.Node.Models[modelB])
	require.NoError(t, manager.SetAppliedDeployment(context.Background(), node.Node.Id, apiconfig.AppliedDeploymentState{
		ModelID:     modelB,
		Fingerprint: appliedB.Fingerprint(),
	}))
	got, found, err := manager.GetAppliedDeployment(context.Background(), node.Node.Id)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, modelB, got.ModelID)

	node.State.EpochModels = map[string]types.Model{modelA: {Id: modelA}}
	node.State.EpochMLNodes = map[string]types.MLNodeInfo{modelA: {NodeId: node.Node.Id}}
	node.State.DeploymentUpdatePending = false
	b.refreshDeploymentUpdatePendingFromApplied(node.Node.Id)
	require.True(t, node.State.DeploymentUpdatePending)
}

func TestDefaultDeploymentRecordsAppliedState(t *testing.T) {
	manager := testDeploymentConfigManager(t)

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

	command := NewUpdateNodeResultCommand(node.Node.Id, NodeResult{
		Succeeded:             true,
		FinalStatus:           types.HardwareNodeStatus_INFERENCE,
		OriginalTarget:        types.HardwareNodeStatus_INFERENCE,
		FinalPocStatus:        PocStatusIdle,
		OriginalPocTarget:     PocStatusIdle,
		DeploymentApplied:     true,
		DeploymentModelID:     modelID,
		DeploymentFingerprint: "default-fingerprint",
	})
	command.Execute(b)

	got, found, err := manager.GetAppliedDeployment(context.Background(), node.Node.Id)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, modelID, got.ModelID)
	require.Equal(t, "default-fingerprint", got.Fingerprint)
	require.False(t, node.State.DeploymentUpdatePending)
}

func TestPersistenceFailureKeepsDeploymentPending(t *testing.T) {
	manager := testDeploymentConfigManager(t)
	require.NoError(t, manager.SqlDb().GetDb().Close())

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

	command := NewUpdateNodeResultCommand(node.Node.Id, NodeResult{
		Succeeded:             true,
		FinalStatus:           types.HardwareNodeStatus_INFERENCE,
		OriginalTarget:        types.HardwareNodeStatus_INFERENCE,
		FinalPocStatus:        PocStatusIdle,
		OriginalPocTarget:     PocStatusIdle,
		DeploymentApplied:     true,
		DeploymentModelID:     "model-a",
		DeploymentFingerprint: "fp",
	})
	command.Execute(b)

	require.True(t, node.State.DeploymentUpdatePending)
}

func TestRejectedDeploymentDoesNotPersistFingerprint(t *testing.T) {
	manager := testDeploymentConfigManager(t)

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
	require.NoError(t, manager.SetAppliedDeployment(context.Background(), node.Node.Id, apiconfig.AppliedDeploymentState{
		ModelID:     "model-a",
		Fingerprint: "old-commit",
	}))

	command := NewUpdateNodeResultCommand(node.Node.Id, NodeResult{
		Succeeded:         false,
		FinalStatus:       types.HardwareNodeStatus_FAILED,
		OriginalTarget:    types.HardwareNodeStatus_INFERENCE,
		OriginalPocTarget: PocStatusIdle,
		Error:             "start inference failed with HTTP 409",
	})
	command.Execute(b)

	got, found, err := manager.GetAppliedDeployment(context.Background(), node.Node.Id)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "old-commit", got.Fingerprint)
	require.True(t, node.State.DeploymentUpdatePending)
}

func TestRemovedOverrideWithStaleFingerprintMarksNodeDirty(t *testing.T) {
	manager := testDeploymentConfigManager(t)

	const modelID = "MiniMaxAI/MiniMax-M2.7"
	node := createTestNodeWithStatus("node-1", types.HardwareNodeStatus_INFERENCE)
	node.Node.Models = map[string]ModelArgs{modelID: {}}
	node.State.EpochModels[modelID] = types.Model{Id: modelID}
	node.State.EpochMLNodes[modelID] = types.MLNodeInfo{NodeId: node.Node.Id}
	b := &Broker{
		nodes:         map[string]*NodeWithState{node.Node.Id: node},
		configManager: manager,
	}
	require.NoError(t, manager.SetAppliedDeployment(
		context.Background(), node.Node.Id, apiconfig.AppliedDeploymentState{
			ModelID:     modelID,
			Fingerprint: "old-override",
		},
	))

	b.refreshDeploymentUpdatePendingFromApplied(node.Node.Id)

	require.True(t, node.State.DeploymentUpdatePending)
}

func TestCommitOnlyFingerprintChangeMarksNodeDirty(t *testing.T) {
	manager := testDeploymentConfigManager(t)

	const modelID = "MiniMaxAI/MiniMax-M2.7"
	node := createTestNodeWithStatus("node-1", types.HardwareNodeStatus_INFERENCE)
	node.Node.Models = map[string]ModelArgs{
		modelID: {
			ModelOverride: &apiconfig.ModelOverride{
				HfRepo:   "host/custom-minimax",
				HfCommit: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			},
		},
	}
	node.State.EpochModels[modelID] = types.Model{Id: modelID}
	node.State.EpochMLNodes[modelID] = types.MLNodeInfo{NodeId: node.Node.Id}
	b := &Broker{
		nodes:         map[string]*NodeWithState{node.Node.Id: node},
		configManager: manager,
	}
	old := b.ResolveModelDeployment(node.State.EpochModels[modelID], node.Node.Models[modelID])
	require.NoError(t, manager.SetAppliedDeployment(context.Background(), node.Node.Id, apiconfig.AppliedDeploymentState{
		ModelID:     modelID,
		Fingerprint: old.Fingerprint(),
	}))

	node.Node.Models[modelID] = ModelArgs{
		ModelOverride: &apiconfig.ModelOverride{
			HfRepo:   "host/custom-minimax",
			HfCommit: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		},
	}
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

func TestUpdateNode_ActiveModelChangeMarksPending(t *testing.T) {
	b := NewTestBroker()
	node := createTestNodeWithStatus("node1", types.HardwareNodeStatus_INFERENCE)
	node.Node.Id = "node1"
	node.Node.Models = map[string]ModelArgs{"model1": {Args: []string{"--old"}}}
	node.State.EpochMLNodes = map[string]types.MLNodeInfo{"model1": {NodeId: "node1"}}
	node.State.EpochModels = map[string]types.Model{"model1": {Id: "model1"}}
	b.nodes[node.Node.Id] = node

	cmd := NewUpdateNodeCommand(apiconfig.InferenceNodeConfig{
		Id:            "node1",
		Host:          node.Node.Host,
		InferencePort: node.Node.InferencePort,
		PoCPort:       node.Node.PoCPort,
		MaxConcurrent: node.Node.MaxConcurrent,
		Models:        map[string]apiconfig.ModelConfig{"model1": {Args: []string{"--new"}}},
	})
	cmd.Execute(b)
	resp := <-cmd.Response
	require.NoError(t, resp.Error)
	require.True(t, node.State.DeploymentUpdatePending)
}

func TestRefreshDoesNotMarkPendingWhenAssignedModelUnsupported(t *testing.T) {
	manager := testDeploymentConfigManager(t)
	node := createTestNodeWithStatus("node-1", types.HardwareNodeStatus_INFERENCE)
	node.Node.Models = map[string]ModelArgs{"model-b": {}}
	node.State.EpochModels["model-a"] = types.Model{Id: "model-a"}
	node.State.EpochMLNodes["model-a"] = types.MLNodeInfo{NodeId: node.Node.Id}
	b := &Broker{
		nodes:         map[string]*NodeWithState{node.Node.Id: node},
		configManager: manager,
	}

	b.refreshDeploymentUpdatePendingFromApplied(node.Node.Id)
	require.False(t, node.State.DeploymentUpdatePending)
}
