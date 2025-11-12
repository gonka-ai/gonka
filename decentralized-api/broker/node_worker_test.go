package broker

import (
	"context"
	"decentralized-api/mlnodeclient"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/assert"
)

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
	for i := 0; i < 25; i++ { // Queue size is 10, but we submit 10
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

	// Only 10 should succeed
	assert.Equal(t, 10, slowCmdSubmitted, "Should submit exactly 10 commands (queue size)")
	assert.Equal(t, 15, slowCmdFailed, "Should fail exactly 15 commands (beyond queue size)")
}

func TestNodeWorker_GracefulShutdown(t *testing.T) {
	broker := NewTestBroker2(10)
	node := createTestNode("test-node-1")
	mockClient := mlnodeclient.NewMockClient()
	worker := NewNodeWorkerWithClient("test-node-1", node, mockClient, broker)

	// Submit commands that will execute during shutdown
	var executedCount int32
	for i := 0; i < 5; i++ {
		cmd := &TestCommand{
			ExecuteFn: func(ctx context.Context, worker *NodeWorker) NodeResult {
				atomic.AddInt32(&executedCount, 1)
				time.Sleep(10 * time.Millisecond)
				return NodeResult{Succeeded: true}
			},
		}
		worker.Submit(context.Background(), cmd)
	}

	// Give first command time to start
	time.Sleep(5 * time.Millisecond)

	// Shutdown should wait for all commands
	worker.Shutdown()

	assert.Equal(t, int32(5), atomic.LoadInt32(&executedCount),
		"All queued commands should execute before shutdown completes")

	assert.Len(t, broker.highPriorityCommands, 5, "Should have 5 results in broker channel")
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
	assert.Equal(t, 2, mockClient.StopCalled, "Stop should be called again for inference up")
	assert.Equal(t, 1, mockClient.InferenceUpCalled, "InferenceUp should be called once")
	assert.Equal(t, "test-model", mockClient.LastInferenceModel, "Model should be captured")
	assert.Equal(t, []string{"--arg1", "--arg2"}, mockClient.LastInferenceArgs, "Args should be captured")
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
	version2Client := mockFactory.CreateClient(versionedPocUrl2, "", "").(*mlnodeclient.MockClient)
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
