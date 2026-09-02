package broker

import (
	"context"
	"decentralized-api/apiconfig"
	"decentralized-api/chainphase"
	"decentralized-api/mlnodeclient"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type noModelCacheClient struct {
	*mlnodeclient.MockClient
}

func (c *noModelCacheClient) CheckModelStatus(context.Context, mlnodeclient.Model) (*mlnodeclient.ModelStatusResponse, error) {
	return nil, mlnodeclient.NewAPINotImplementedError("/api/v1/models/status", http.StatusNotFound)
}

func createTestNode(id string) *NodeWithState {
	return createTestNodeWithStatus(id, types.HardwareNodeStatus_UNKNOWN)
}

func createTestNodeWithStatus(id string, status types.HardwareNodeStatus) *NodeWithState {
	return &NodeWithState{
		Node: Node{
			Id:               id,
			Host:             "test-host",
			InferencePort:    8080,
			PoCPort:          8081,
			InferenceSegment: "/inference",
			PoCSegment:       "/poc",
			MaxConcurrent:    5,
			NodeNum:          1,
		},
		State: NodeState{
			CurrentStatus:  status,
			IntendedStatus: status,
			AdminState: AdminState{
				Enabled: true,
				Epoch:   0,
			},
			EpochModels:  make(map[string]types.Model),
			EpochMLNodes: make(map[string]types.MLNodeInfo),
		},
	}
}

func NewTestBroker2(cap int) *Broker {
	return &Broker{
		highPriorityCommands: make(chan Command, cap),
		lowPriorityCommands:  make(chan Command, cap),
	}
}

func TestNodeWorker_BasicOperation(t *testing.T) {
	broker := NewTestBroker2(1)
	node := createTestNode("test-node-1")
	mockClient := mlnodeclient.NewMockClient()
	worker := NewNodeWorkerWithClient("test-node-1", node, mockClient, broker)
	defer worker.Shutdown()

	// Test successful command submission
	cmd := &TestCommand{
		ExecuteFn: func(ctx context.Context, worker *NodeWorker) NodeResult {
			return NodeResult{Succeeded: true, FinalStatus: types.HardwareNodeStatus_STOPPED}
		},
	}
	success := worker.Submit(context.Background(), cmd)
	assert.True(t, success, "Command submission should succeed")

	// Wait for command execution and result submission
	select {
	case receivedCmd := <-broker.highPriorityCommands:
		updateCmd, ok := receivedCmd.(UpdateNodeResultCommand)
		assert.True(t, ok, "Broker should receive an UpdateNodeResultCommand")
		assert.Equal(t, "test-node-1", updateCmd.NodeId)
		assert.True(t, updateCmd.Result.Succeeded)
		assert.Equal(t, types.HardwareNodeStatus_STOPPED, updateCmd.Result.FinalStatus)
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timed out waiting for broker to receive command")
	}
}

func TestNodeWorker_StampsDeploymentGenerationOnResult(t *testing.T) {
	broker := NewTestBroker2(1)
	node := createTestNode("test-node-1")
	node.State.RegistrationSeq = 9
	mockClient := mlnodeclient.NewMockClient()
	worker := NewNodeWorkerWithClient("test-node-1", node, mockClient, broker)
	defer worker.Shutdown()

	cmd := &TestCommand{
		ExecuteFn: func(ctx context.Context, worker *NodeWorker) NodeResult {
			return NodeResult{Succeeded: true, FinalStatus: types.HardwareNodeStatus_INFERENCE}
		},
	}
	require.True(t, worker.submit(context.Background(), cmd, 7))

	select {
	case receivedCmd := <-broker.highPriorityCommands:
		updateCmd, ok := receivedCmd.(UpdateNodeResultCommand)
		require.True(t, ok)
		require.Equal(t, uint64(7), updateCmd.Result.DeploymentGeneration)
		require.Equal(t, uint64(9), updateCmd.Result.RegistrationSeq)
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timed out waiting for broker to receive command")
	}
}

func TestNodeWorker_ErrorHandling(t *testing.T) {
	broker := NewTestBroker2(1)
	node := createTestNode("test-node-1")
	mockClient := mlnodeclient.NewMockClient()
	worker := NewNodeWorkerWithClient("test-node-1", node, mockClient, broker)
	defer worker.Shutdown()

	// Submit command that returns error
	testErr := errors.New("test error")
	cmd := &TestCommand{
		ExecuteFn: func(ctx context.Context, worker *NodeWorker) NodeResult {
			return NodeResult{Succeeded: false, Error: testErr.Error()}
		},
	}
	success := worker.Submit(context.Background(), cmd)
	assert.True(t, success, "Command submission should succeed")

	// Wait for command execution and result submission
	select {
	case receivedCmd := <-broker.highPriorityCommands:
		updateCmd, ok := receivedCmd.(UpdateNodeResultCommand)
		assert.True(t, ok, "Broker should receive an UpdateNodeResultCommand")
		assert.Equal(t, "test-node-1", updateCmd.NodeId)
		assert.False(t, updateCmd.Result.Succeeded)
		assert.Equal(t, "test error", updateCmd.Result.Error)
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timed out waiting for broker to receive command")
	}
}

func TestNodeWorker_QueueFull(t *testing.T) {
	broker := NewTestBroker2(20) // Make it larger to handle results
	node := createTestNode("test-node-1")
	mockClient := mlnodeclient.NewMockClient()
	worker := NewNodeWorkerWithClient("test-node-1", node, mockClient, broker)
	defer worker.Shutdown()

	// Fill the queue with slow commands
	slowCmdSubmitted := 0
	slowCmdFailed := 0
	for i := 0; i < 25; i++ {
		cmd := &TestCommand{
			ExecuteFn: func(ctx context.Context, worker *NodeWorker) NodeResult {
				time.Sleep(100 * time.Millisecond)
				return NodeResult{Succeeded: true}
			},
		}
		success := worker.Submit(context.Background(), cmd)
		if success {
			slowCmdSubmitted++
		} else {
			slowCmdFailed++
		}
	}

	// Queue size is 10. The run loop may already be executing one command,
	// so 10 queued plus 1 in flight can all succeed.
	assert.GreaterOrEqual(t, slowCmdSubmitted, 10)
	assert.LessOrEqual(t, slowCmdSubmitted, 11)
	assert.Equal(t, 25, slowCmdSubmitted+slowCmdFailed)
}

func TestNodeWorker_ShutdownDropsQueuedCommands(t *testing.T) {
	broker := NewTestBroker2(10)
	node := createTestNode("test-node-1")
	mockClient := mlnodeclient.NewMockClient()
	worker := NewNodeWorkerWithClient("test-node-1", node, mockClient, broker)

	started := make(chan struct{})
	inFlight := &TestCommand{
		ExecuteFn: func(ctx context.Context, worker *NodeWorker) NodeResult {
			close(started)
			time.Sleep(30 * time.Millisecond)
			return NodeResult{Succeeded: true}
		},
	}
	require.True(t, worker.Submit(context.Background(), inFlight))
	<-started

	var queuedExecuted int32
	for i := 0; i < 4; i++ {
		cmd := &TestCommand{
			ExecuteFn: func(ctx context.Context, worker *NodeWorker) NodeResult {
				atomic.AddInt32(&queuedExecuted, 1)
				return NodeResult{Succeeded: true}
			},
		}
		require.True(t, worker.Submit(context.Background(), cmd))
	}
	require.NotZero(t, len(worker.commands))

	worker.signalShutdown()
	require.Equal(t, 0, len(worker.commands), "queued commands must be drained before in-flight HTTP returns")

	done := make(chan struct{})
	go func() {
		worker.Shutdown()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Shutdown blocked on queued worker commands")
	}

	assert.Equal(t, int32(0), atomic.LoadInt32(&queuedExecuted),
		"queued commands must be dropped, not executed, on shutdown")
}

func TestNodeWorker_SubmitRejectedAfterShutdown(t *testing.T) {
	broker := NewTestBroker2(4)
	node := createTestNode("test-node-1")
	worker := NewNodeWorkerWithClient("test-node-1", node, mlnodeclient.NewMockClient(), broker)

	worker.Shutdown()

	accepted := worker.Submit(context.Background(), &TestCommand{
		ExecuteFn: func(ctx context.Context, worker *NodeWorker) NodeResult {
			t.Error("command must not execute after shutdown")
			return NodeResult{Succeeded: true}
		},
	})
	assert.False(t, accepted, "Submit after shutdown must be rejected")
}

func TestNodeWorker_ShutdownDropsCommandWhenSelectPicksWork(t *testing.T) {
	broker := NewTestBroker2(16)
	node := createTestNode("test-node-1")
	worker := NewNodeWorkerWithClient("test-node-1", node, mlnodeclient.NewMockClient(), broker)

	var executed int32
	for i := 0; i < 10; i++ {
		cmd := &TestCommand{
			ExecuteFn: func(ctx context.Context, worker *NodeWorker) NodeResult {
				atomic.AddInt32(&executed, 1)
				time.Sleep(20 * time.Millisecond)
				return NodeResult{Succeeded: true}
			},
		}
		require.True(t, worker.Submit(context.Background(), cmd))
	}

	worker.Shutdown()

	assert.LessOrEqual(t, atomic.LoadInt32(&executed), int32(1),
		"at most the in-flight command may run; queued work must be dropped after shutdown")
}

func TestNodeWorker_Cancellation(t *testing.T) {
	broker := NewTestBroker2(1)
	node := createTestNode("test-node-1")
	mockClient := mlnodeclient.NewMockClient()
	worker := NewNodeWorkerWithClient("test-node-1", node, mockClient, broker)
	defer worker.Shutdown()

	cmdStarted := make(chan struct{})
	cmd := &TestCommand{
		ExecuteFn: func(ctx context.Context, worker *NodeWorker) NodeResult {
			close(cmdStarted)
			<-ctx.Done() // Wait for cancellation
			return NodeResult{
				Succeeded:      false,
				Error:          ctx.Err().Error(),
				FinalStatus:    worker.node.State.CurrentStatus,
				OriginalTarget: types.HardwareNodeStatus_STOPPED,
			}
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	worker.Submit(ctx, cmd)

	<-cmdStarted // Ensure command has started execution
	cancel()     // Cancel it

	select {
	case receivedCmd := <-broker.highPriorityCommands:
		updateCmd, ok := receivedCmd.(UpdateNodeResultCommand)
		assert.True(t, ok)
		assert.False(t, updateCmd.Result.Succeeded)
		assert.Equal(t, context.Canceled.Error(), updateCmd.Result.Error)
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timed out waiting for cancelled command result")
	}
}

func TestNodeWorker_MLClientInteraction(t *testing.T) {
	broker := NewTestBroker2(5)
	node := createTestNode("test-node-1")
	mockClient := mlnodeclient.NewMockClient()
	worker := NewNodeWorkerWithClient("test-node-1", node, mockClient, broker)
	defer worker.Shutdown()

	// Test Stop operation
	stopCmd := StopNodeCommand{}
	worker.Submit(context.Background(), &stopCmd)

	select {
	case receivedCmd := <-broker.highPriorityCommands:
		updateCmd, ok := receivedCmd.(UpdateNodeResultCommand)
		assert.True(t, ok)
		assert.True(t, updateCmd.Result.Succeeded)
		assert.Equal(t, types.HardwareNodeStatus_STOPPED, updateCmd.Result.FinalStatus)
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timed out waiting for stop command result")
	}
	assert.Equal(t, 1, mockClient.StopCalled, "Stop should be called once")

	// Test InferenceUp operation
	node.Node.Models = map[string]ModelArgs{
		"test-model": {Args: []string{"--arg1", "--arg2"}},
	}
	// Manually populate the EpochModels for this test, as suggested.
	node.State.EpochModels["test-model"] = types.Model{Id: "test-model", ModelArgs: []string{"--arg1", "--arg2"}}
	node.State.EpochMLNodes["test-model"] = types.MLNodeInfo{
		NodeId: "test-node-1",
	}
	inferenceCmd := InferenceUpNodeCommand{}
	worker.Submit(context.Background(), &inferenceCmd)

	select {
	case receivedCmd := <-broker.highPriorityCommands:
		updateCmd, ok := receivedCmd.(UpdateNodeResultCommand)
		assert.True(t, ok)
		assert.True(t, updateCmd.Result.Succeeded)
		assert.Equal(t, types.HardwareNodeStatus_INFERENCE, updateCmd.Result.FinalStatus)
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timed out waiting for inference up command result")
	}
	mockClient.Mu.Lock()
	stopCalled := mockClient.StopCalled
	mockClient.Mu.Unlock()
	assert.Equal(t, 2, stopCalled, "Stop should be called again for inference up")
	assert.Equal(t, 1, mockClient.GetInferenceUpCalled(), "InferenceUp should be called once")
	assert.Equal(t, "test-model", mockClient.LastInferenceModel, "Model should be captured")
	assert.Equal(t, []string{"--arg1", "--arg2"}, mockClient.LastInferenceArgs, "Args should be captured")
}

func TestInferenceUpNodeCommand_DeploysOverrideWithAlias(t *testing.T) {
	b := NewTestBroker2(5)
	node := createTestNodeWithStatus("test-node-1", types.HardwareNodeStatus_STOPPED)
	commit := "0123456789abcdef0123456789abcdef01234567"
	node.Node.Models = map[string]ModelArgs{
		"MiniMaxAI/MiniMax-M2.7": {
			ModelOverride: &apiconfig.ModelOverride{
				HfRepo:   "host/custom-minimax",
				HfCommit: commit,
			},
		},
	}
	node.State.IntendedStatus = types.HardwareNodeStatus_INFERENCE
	node.State.EpochModels["MiniMaxAI/MiniMax-M2.7"] = types.Model{Id: "MiniMaxAI/MiniMax-M2.7"}
	node.State.EpochMLNodes["MiniMaxAI/MiniMax-M2.7"] = types.MLNodeInfo{NodeId: node.Node.Id}
	client := mlnodeclient.NewMockClient()
	worker := NewNodeWorkerWithClient(node.Node.Id, node, client, b)
	defer worker.Shutdown()

	result := (InferenceUpNodeCommand{}).Execute(context.Background(), worker)

	require.True(t, result.Succeeded)
	require.True(t, result.DeploymentApplied)
	require.True(t, result.DeploymentUsesOverride)
	require.NotEmpty(t, result.DeploymentFingerprint)
	require.Equal(t, "MiniMaxAI/MiniMax-M2.7", result.DeploymentModelID)
	require.Equal(t, "host/custom-minimax", client.LastInferenceModel)
	require.Equal(t, []string{
		"--revision", commit,
		"--served-model-name", "MiniMaxAI/MiniMax-M2.7",
	}, client.LastInferenceArgs)
}

func TestInferenceUpNodeCommand_HealthyNodeSurvivesModelResolutionFailure(t *testing.T) {
	b := NewTestBroker2(5)
	node := createTestNodeWithStatus("test-node-1", types.HardwareNodeStatus_INFERENCE)
	client := mlnodeclient.NewMockClient()
	client.CurrentState = mlnodeclient.MlNodeState_INFERENCE
	client.InferenceIsHealthy = true
	worker := NewNodeWorkerWithClient(node.Node.Id, node, client, b)
	defer worker.Shutdown()

	result := (InferenceUpNodeCommand{}).Execute(context.Background(), worker)

	require.True(t, result.Succeeded)
	require.Equal(t, types.HardwareNodeStatus_INFERENCE, result.FinalStatus)
	require.Equal(t, 0, client.GetStopCalled())
	require.Equal(t, 0, client.GetInferenceUpCalled())
}

func TestInferenceUpNodeCommand_KeepsHealthyWhenAssignedModelUnsupported(t *testing.T) {
	b := NewTestBroker2(5)
	node := createTestNodeWithStatus("test-node-1", types.HardwareNodeStatus_INFERENCE)
	node.Node.Models = map[string]ModelArgs{"model-b": {}}
	node.State.EpochModels["model-a"] = types.Model{Id: "model-a"}
	node.State.EpochMLNodes["model-a"] = types.MLNodeInfo{NodeId: node.Node.Id}
	client := mlnodeclient.NewMockClient()
	client.CurrentState = mlnodeclient.MlNodeState_INFERENCE
	client.InferenceIsHealthy = true
	worker := NewNodeWorkerWithClient(node.Node.Id, node, client, b)
	defer worker.Shutdown()

	result := (InferenceUpNodeCommand{}).Execute(context.Background(), worker)

	require.True(t, result.Succeeded)
	require.False(t, result.DeploymentApplied)
	require.Equal(t, types.HardwareNodeStatus_INFERENCE, result.FinalStatus)
	require.Equal(t, 0, client.GetStopCalled())
	require.Equal(t, 0, client.GetInferenceUpCalled())
}

func TestInferenceUpNodeCommand_DirtyNodeFailsModelResolutionFailure(t *testing.T) {
	b := NewTestBroker2(5)
	node := createTestNodeWithStatus("test-node-1", types.HardwareNodeStatus_INFERENCE)
	node.State.DeploymentUpdatePending = true
	client := mlnodeclient.NewMockClient()
	client.CurrentState = mlnodeclient.MlNodeState_INFERENCE
	client.InferenceIsHealthy = true
	worker := NewNodeWorkerWithClient(node.Node.Id, node, client, b)
	defer worker.Shutdown()

	result := (InferenceUpNodeCommand{}).Execute(context.Background(), worker)

	require.False(t, result.Succeeded)
	require.Equal(t, types.HardwareNodeStatus_FAILED, result.FinalStatus)
	require.Equal(t, 0, client.GetStopCalled())
}

func TestInferenceUpNodeCommand_DefersHealthyOverrideUntilDownloaded(t *testing.T) {
	b := NewTestBroker2(5)
	node := createTestNodeWithStatus("test-node-1", types.HardwareNodeStatus_INFERENCE)
	commit := "0123456789abcdef0123456789abcdef01234567"
	node.Node.Models = map[string]ModelArgs{
		"MiniMaxAI/MiniMax-M2.7": {
			ModelOverride: &apiconfig.ModelOverride{
				HfRepo:   "host/custom-minimax",
				HfCommit: commit,
			},
		},
	}
	node.State.DeploymentUpdatePending = true
	node.State.EpochModels["MiniMaxAI/MiniMax-M2.7"] = types.Model{Id: "MiniMaxAI/MiniMax-M2.7"}
	node.State.EpochMLNodes["MiniMaxAI/MiniMax-M2.7"] = types.MLNodeInfo{NodeId: node.Node.Id}
	client := mlnodeclient.NewMockClient()
	client.CurrentState = mlnodeclient.MlNodeState_INFERENCE
	client.InferenceIsHealthy = true
	client.LastInferenceModel = "MiniMaxAI/MiniMax-M2.7"
	worker := NewNodeWorkerWithClient(node.Node.Id, node, client, b)
	defer worker.Shutdown()

	result := (InferenceUpNodeCommand{}).Execute(context.Background(), worker)

	require.True(t, result.Succeeded)
	require.True(t, result.DeploymentDeferred)
	require.False(t, result.DeploymentApplied)
	require.Equal(t, types.HardwareNodeStatus_INFERENCE, result.FinalStatus)
	require.Equal(t, 0, client.GetStopCalled())
	require.NotNil(t, client.LastModelDownload)
	require.Equal(t, "host/custom-minimax", client.LastModelDownload.HfRepo)
}

func TestInferenceUpNodeCommand_OlderMLNodeDoesNotBlockOverride(t *testing.T) {
	b := NewTestBroker2(5)
	node := createTestNodeWithStatus("test-node-1", types.HardwareNodeStatus_INFERENCE)
	node.Node.Models = map[string]ModelArgs{
		"MiniMaxAI/MiniMax-M2.7": {
			ModelOverride: &apiconfig.ModelOverride{HfRepo: "host/custom-minimax"},
		},
	}
	node.State.DeploymentUpdatePending = true
	node.State.EpochModels["MiniMaxAI/MiniMax-M2.7"] = types.Model{Id: "MiniMaxAI/MiniMax-M2.7"}
	node.State.EpochMLNodes["MiniMaxAI/MiniMax-M2.7"] = types.MLNodeInfo{NodeId: node.Node.Id}
	mock := mlnodeclient.NewMockClient()
	mock.CurrentState = mlnodeclient.MlNodeState_INFERENCE
	mock.InferenceIsHealthy = true
	mock.LastInferenceModel = "MiniMaxAI/MiniMax-M2.7"
	client := &noModelCacheClient{MockClient: mock}
	worker := NewNodeWorkerWithClient(node.Node.Id, node, client, b)
	defer worker.Shutdown()

	result := (InferenceUpNodeCommand{}).Execute(context.Background(), worker)

	require.True(t, result.Succeeded)
	require.True(t, result.DeploymentApplied)
	require.Equal(t, 1, mock.GetStopCalled())
	require.Equal(t, "host/custom-minimax", mock.LastInferenceModel)
}

func TestInferenceUpNodeCommand_RecordsFingerprintForDefaultDeployment(t *testing.T) {
	b := NewTestBroker2(5)
	node := createTestNodeWithStatus("test-node-1", types.HardwareNodeStatus_STOPPED)
	node.Node.Models = map[string]ModelArgs{"MiniMaxAI/MiniMax-M2.7": {}}
	node.State.EpochModels["MiniMaxAI/MiniMax-M2.7"] = types.Model{Id: "MiniMaxAI/MiniMax-M2.7"}
	node.State.EpochMLNodes["MiniMaxAI/MiniMax-M2.7"] = types.MLNodeInfo{NodeId: node.Node.Id}
	client := mlnodeclient.NewMockClient()
	worker := NewNodeWorkerWithClient(node.Node.Id, node, client, b)
	defer worker.Shutdown()

	result := (InferenceUpNodeCommand{}).Execute(context.Background(), worker)

	require.True(t, result.Succeeded)
	require.True(t, result.DeploymentApplied)
	require.False(t, result.DeploymentUsesOverride)
	require.NotEmpty(t, result.DeploymentFingerprint)
	require.Equal(t, "MiniMaxAI/MiniMax-M2.7", result.DeploymentModelID)
}

func TestInferenceUpNodeCommand_RejectedStopDoesNotStartInference(t *testing.T) {
	b := NewTestBroker2(5)
	node := createTestNodeWithStatus("test-node-1", types.HardwareNodeStatus_INFERENCE)
	node.Node.Models = map[string]ModelArgs{
		"MiniMaxAI/MiniMax-M2.7": {
			ModelOverride: &apiconfig.ModelOverride{HfRepo: "host/custom-minimax"},
		},
	}
	node.State.DeploymentUpdatePending = true
	node.State.EpochModels["MiniMaxAI/MiniMax-M2.7"] = types.Model{Id: "MiniMaxAI/MiniMax-M2.7"}
	node.State.EpochMLNodes["MiniMaxAI/MiniMax-M2.7"] = types.MLNodeInfo{NodeId: node.Node.Id}
	client := mlnodeclient.NewMockClient()
	client.CurrentState = mlnodeclient.MlNodeState_INFERENCE
	client.InferenceIsHealthy = true
	client.LastInferenceModel = "host/old-minimax"
	client.CachedModels["host/custom-minimax:latest"] = mlnodeclient.ModelListItem{
		Status: mlnodeclient.ModelStatusDownloaded,
	}
	client.StopError = errors.New("stop inference failed with HTTP 500")
	worker := NewNodeWorkerWithClient(node.Node.Id, node, client, b)
	defer worker.Shutdown()

	result := (InferenceUpNodeCommand{}).Execute(context.Background(), worker)

	require.False(t, result.Succeeded)
	require.False(t, result.DeploymentApplied)
	require.Equal(t, 1, client.GetStopCalled())
	require.Equal(t, 0, client.GetInferenceUpCalled())
}

func TestInferenceUpNodeCommand_RejectedStartIsNotApplied(t *testing.T) {
	b := NewTestBroker2(5)
	node := createTestNodeWithStatus("test-node-1", types.HardwareNodeStatus_STOPPED)
	node.Node.Models = map[string]ModelArgs{
		"MiniMaxAI/MiniMax-M2.7": {
			ModelOverride: &apiconfig.ModelOverride{
				HfRepo:   "host/custom-minimax",
				HfCommit: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			},
		},
	}
	node.State.EpochModels["MiniMaxAI/MiniMax-M2.7"] = types.Model{Id: "MiniMaxAI/MiniMax-M2.7"}
	node.State.EpochMLNodes["MiniMaxAI/MiniMax-M2.7"] = types.MLNodeInfo{NodeId: node.Node.Id}
	client := mlnodeclient.NewMockClient()
	client.InferenceUpError = errors.New("start inference failed with HTTP 409")
	worker := NewNodeWorkerWithClient(node.Node.Id, node, client, b)
	defer worker.Shutdown()

	result := (InferenceUpNodeCommand{}).Execute(context.Background(), worker)

	require.False(t, result.Succeeded)
	require.False(t, result.DeploymentApplied)
	require.Equal(t, 1, client.GetStopCalled())
	require.Equal(t, 1, client.GetInferenceUpCalled())
}

func TestInferenceUpNodeCommand_DefersOverrideRemovalUntilGovernanceDownloaded(t *testing.T) {
	b := NewTestBroker2(5)
	node := createTestNodeWithStatus("test-node-1", types.HardwareNodeStatus_INFERENCE)
	node.Node.Models = map[string]ModelArgs{"MiniMaxAI/MiniMax-M2.7": {}}
	node.State.DeploymentUpdatePending = true
	node.State.EpochModels["MiniMaxAI/MiniMax-M2.7"] = types.Model{Id: "MiniMaxAI/MiniMax-M2.7"}
	node.State.EpochMLNodes["MiniMaxAI/MiniMax-M2.7"] = types.MLNodeInfo{NodeId: node.Node.Id}
	client := mlnodeclient.NewMockClient()
	client.CurrentState = mlnodeclient.MlNodeState_INFERENCE
	client.InferenceIsHealthy = true
	client.LastInferenceModel = "host/custom-minimax"
	client.LastInferenceArgs = []string{"--served-model-name", "MiniMaxAI/MiniMax-M2.7"}
	worker := NewNodeWorkerWithClient(node.Node.Id, node, client, b)
	defer worker.Shutdown()

	result := (InferenceUpNodeCommand{}).Execute(context.Background(), worker)

	require.True(t, result.Succeeded)
	require.True(t, result.DeploymentDeferred)
	require.False(t, result.DeploymentApplied)
	require.Equal(t, 0, client.GetStopCalled())
	require.Equal(t, 0, client.GetInferenceUpCalled())
	require.NotNil(t, client.LastModelDownload)
	require.Equal(t, "MiniMaxAI/MiniMax-M2.7", client.LastModelDownload.HfRepo)
}

func TestInferenceUpNodeCommand_RemovesOverrideOnceGovernanceIsCached(t *testing.T) {
	b := NewTestBroker2(5)
	node := createTestNodeWithStatus("test-node-1", types.HardwareNodeStatus_INFERENCE)
	node.Node.Models = map[string]ModelArgs{"MiniMaxAI/MiniMax-M2.7": {}}
	node.State.DeploymentUpdatePending = true
	node.State.EpochModels["MiniMaxAI/MiniMax-M2.7"] = types.Model{Id: "MiniMaxAI/MiniMax-M2.7"}
	node.State.EpochMLNodes["MiniMaxAI/MiniMax-M2.7"] = types.MLNodeInfo{NodeId: node.Node.Id}
	client := mlnodeclient.NewMockClient()
	client.CurrentState = mlnodeclient.MlNodeState_INFERENCE
	client.InferenceIsHealthy = true
	client.LastInferenceModel = "host/custom-minimax"
	client.LastInferenceArgs = []string{"--served-model-name", "MiniMaxAI/MiniMax-M2.7"}
	client.CachedModels["MiniMaxAI/MiniMax-M2.7:latest"] = mlnodeclient.ModelListItem{
		Status: mlnodeclient.ModelStatusDownloaded,
	}
	worker := NewNodeWorkerWithClient(node.Node.Id, node, client, b)
	defer worker.Shutdown()

	result := (InferenceUpNodeCommand{}).Execute(context.Background(), worker)

	require.True(t, result.Succeeded)
	require.True(t, result.DeploymentApplied)
	require.False(t, result.DeploymentDeferred)
	require.Equal(t, 1, client.GetStopCalled())
	require.Equal(t, 1, client.GetInferenceUpCalled())
	require.Equal(t, "MiniMaxAI/MiniMax-M2.7", client.LastInferenceModel)
}

func TestInferenceUpNodeCommand_OlderMLNodeRemovesOverrideDirectly(t *testing.T) {
	b := NewTestBroker2(5)
	node := createTestNodeWithStatus("test-node-1", types.HardwareNodeStatus_INFERENCE)
	node.Node.Models = map[string]ModelArgs{"MiniMaxAI/MiniMax-M2.7": {}}
	node.State.DeploymentUpdatePending = true
	node.State.EpochModels["MiniMaxAI/MiniMax-M2.7"] = types.Model{Id: "MiniMaxAI/MiniMax-M2.7"}
	node.State.EpochMLNodes["MiniMaxAI/MiniMax-M2.7"] = types.MLNodeInfo{NodeId: node.Node.Id}
	mock := mlnodeclient.NewMockClient()
	mock.CurrentState = mlnodeclient.MlNodeState_INFERENCE
	mock.InferenceIsHealthy = true
	mock.LastInferenceModel = "host/custom-minimax"
	mock.LastInferenceArgs = []string{"--served-model-name", "MiniMaxAI/MiniMax-M2.7"}
	client := &noModelCacheClient{MockClient: mock}
	worker := NewNodeWorkerWithClient(node.Node.Id, node, client, b)
	defer worker.Shutdown()

	result := (InferenceUpNodeCommand{}).Execute(context.Background(), worker)

	require.True(t, result.Succeeded)
	require.True(t, result.DeploymentApplied)
	require.Equal(t, 1, mock.GetStopCalled())
	require.Equal(t, "MiniMaxAI/MiniMax-M2.7", mock.LastInferenceModel)
}

func TestNodeWorkGroup_AddRemoveWorkers(t *testing.T) {
	group := NewNodeWorkGroup()
	broker := NewTestBroker2(1)

	// Add workers
	node1 := createTestNode("node-1")
	node2 := createTestNode("node-2")

	worker1 := NewNodeWorkerWithClient("node-1", node1, mlnodeclient.NewMockClient(), broker)
	worker2 := NewNodeWorkerWithClient("node-2", node2, mlnodeclient.NewMockClient(), broker)

	group.AddWorker("node-1", worker1)
	group.AddWorker("node-2", worker2)

	// Check workers exist
	w1, exists1 := group.GetWorker("node-1")
	w2, exists2 := group.GetWorker("node-2")

	assert.True(t, exists1, "Worker 1 should exist")
	assert.True(t, exists2, "Worker 2 should exist")
	assert.Equal(t, worker1, w1)
	assert.Equal(t, worker2, w2)

	// Remove worker
	group.RemoveWorker("node-1")

	_, exists1 = group.GetWorker("node-1")
	assert.False(t, exists1, "Worker 1 should not exist after removal")
}

func TestNodeWorkGroup_RemoveWorkerDoesNotBlockOnInFlightHTTP(t *testing.T) {
	group := NewNodeWorkGroup()
	broker := NewTestBroker2(4)

	hung := createTestNode("hung")
	other := createTestNode("other")
	hungWorker := NewNodeWorkerWithClient("hung", hung, mlnodeclient.NewMockClient(), broker)
	otherWorker := NewNodeWorkerWithClient("other", other, mlnodeclient.NewMockClient(), broker)
	group.AddWorker("hung", hungWorker)
	group.AddWorker("other", otherWorker)

	started := make(chan struct{})
	require.True(t, hungWorker.Submit(context.Background(), &TestCommand{
		ExecuteFn: func(ctx context.Context, w *NodeWorker) NodeResult {
			close(started)
			time.Sleep(300 * time.Millisecond)
			return NodeResult{Succeeded: true}
		},
	}))
	<-started

	removed := make(chan struct{})
	go func() {
		group.RemoveWorker("hung")
		close(removed)
	}()

	select {
	case <-removed:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("RemoveWorker blocked while a worker command was in flight")
	}

	_, hungExists := group.GetWorker("hung")
	assert.False(t, hungExists)
	gotOther, otherExists := group.GetWorker("other")
	assert.True(t, otherExists)
	assert.Equal(t, otherWorker, gotOther)

	accepted := hungWorker.Submit(context.Background(), &TestCommand{
		ExecuteFn: func(ctx context.Context, w *NodeWorker) NodeResult {
			t.Error("command must not start after RemoveWorker returns")
			return NodeResult{Succeeded: true}
		},
	})
	assert.False(t, accepted, "Submit must be rejected as soon as RemoveWorker returns")
}

func TestRemoveWorkerDoesNotBlockWhenResultQueueIsFull(t *testing.T) {
	broker := NewTestBroker2(1)
	broker.phaseTracker = &chainphase.ChainPhaseTracker{}
	broker.nodes = make(map[string]*NodeWithState)

	filler := NewUpdateNodeResultCommand("filler", NodeResult{})
	broker.highPriorityCommands <- filler

	group := NewNodeWorkGroup()
	node := createTestNode("n1")
	node.State.RegistrationSeq = 1
	worker := NewNodeWorkerWithClient("n1", node, mlnodeclient.NewMockClient(), broker)
	group.AddWorker("n1", worker)

	executeDone := make(chan struct{})
	require.True(t, worker.Submit(context.Background(), &TestCommand{
		ExecuteFn: func(ctx context.Context, w *NodeWorker) NodeResult {
			defer close(executeDone)
			return NodeResult{Succeeded: true, FinalStatus: types.HardwareNodeStatus_INFERENCE}
		},
	}))
	<-executeDone
	time.Sleep(30 * time.Millisecond)

	var queuedRan atomic.Bool
	require.True(t, worker.Submit(context.Background(), &TestCommand{
		ExecuteFn: func(ctx context.Context, w *NodeWorker) NodeResult {
			queuedRan.Store(true)
			return NodeResult{Succeeded: true}
		},
	}))

	removed := make(chan struct{})
	go func() {
		group.RemoveWorker("n1")
		close(removed)
	}()
	select {
	case <-removed:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("RemoveWorker blocked while QueueMessage was parked on a full broker channel")
	}

	_, exists := group.GetWorker("n1")
	require.False(t, exists)
	require.False(t, queuedRan.Load(), "queued worker commands must be dropped")

	select {
	case <-broker.highPriorityCommands:
	case <-time.After(time.Second):
		t.Fatal("timed out draining the filler command")
	}

	select {
	case cmd := <-broker.highPriorityCommands:
		update, ok := cmd.(UpdateNodeResultCommand)
		require.True(t, ok)
		require.Equal(t, "n1", update.NodeId)
		require.Equal(t, uint64(1), update.Result.RegistrationSeq)

		replacement := createTestNodeWithStatus("n1", types.HardwareNodeStatus_INFERENCE)
		replacement.State.RegistrationSeq = 2
		replacement.State.ReconcileInfo = &ReconcileInfo{
			Status:     types.HardwareNodeStatus_INFERENCE,
			PocStatus:  PocStatusIdle,
			Generation: 1,
		}
		broker.nodes["n1"] = replacement
		update.Execute(broker)
		require.Equal(t, types.HardwareNodeStatus_INFERENCE, replacement.State.CurrentStatus)
		require.NotNil(t, replacement.State.ReconcileInfo)
	case <-time.After(time.Second):
		t.Fatal("late result was not delivered after the filler was drained")
	}
}

func TestNodeWorker_CheckClientVersionAlive(t *testing.T) {
	broker := NewTestBroker2(1)
	node := createTestNode("test-node-1")
	mainClient := mlnodeclient.NewMockClient()
	mockFactory := mlnodeclient.NewMockClientFactory()

	worker := NewNodeWorkerWithClient("test-node-1", node, mainClient, broker)
	defer worker.Shutdown()

	version := "v1.0.0"
	versionedPocUrl := node.Node.PoCUrlWithVersion(version)

	// --- Test Case 1: Version is alive ---
	versionClient := mockFactory.GetClientForNode(versionedPocUrl)
	assert.Nil(t, versionClient, "Client should not exist yet")

	alive, err := worker.CheckClientVersionAlive(version, mockFactory)
	assert.NoError(t, err)
	assert.True(t, alive)

	versionClient = mockFactory.GetClientForNode(versionedPocUrl)
	assert.NotNil(t, versionClient, "Client should be created for the version")
	assert.Equal(t, 1, versionClient.NodeStateCalled)

	// --- Test Case 2: Check caching - should not call NodeState again ---
	alive, err = worker.CheckClientVersionAlive(version, mockFactory)
	assert.NoError(t, err)
	assert.True(t, alive)
	assert.Equal(t, 1, versionClient.NodeStateCalled, "NodeState should not be called again due to cache")

	// --- Test Case 3: Version is not alive ---
	mockFactory.Reset()
	worker.availableVersions = make(map[string]bool) // Reset internal cache
	version2 := "v2.0.0"
	versionedPocUrl2 := node.Node.PoCUrlWithVersion(version2)

	// Configure the mock client for this version to return an error
	version2Client := mockFactory.CreateClient(versionedPocUrl2, "").(*mlnodeclient.MockClient)
	testErr := errors.New("node not ready")
	version2Client.NodeStateError = testErr

	alive, err = worker.CheckClientVersionAlive(version2, mockFactory)
	assert.Error(t, err)
	assert.Equal(t, testErr, err)
	assert.False(t, alive)
	assert.Equal(t, 1, version2Client.NodeStateCalled)

	// --- Test Case 4: Retry after failure ---
	// It should try again. Let's make it succeed this time.
	version2Client.NodeStateError = nil
	alive, err = worker.CheckClientVersionAlive(version2, mockFactory)
	assert.NoError(t, err)
	assert.True(t, alive)
	assert.Equal(t, 2, version2Client.NodeStateCalled, "NodeState should be called again on retry")
}

// TestCommand is a simple command for testing
type TestCommand struct {
	ExecuteFn func(ctx context.Context, worker *NodeWorker) NodeResult
}

func (c *TestCommand) Execute(ctx context.Context, worker *NodeWorker) NodeResult {
	if c.ExecuteFn != nil {
		return c.ExecuteFn(ctx, worker)
	}
	return NodeResult{Succeeded: true}
}
