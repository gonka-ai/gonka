package broker

import (
	"decentralized-api/mlnodeclient"
	"errors"
	"testing"

	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStartPocCommand_Success(t *testing.T) {
	// Create test nodes
	node1 := createTestNode("node-1")
	node2 := createTestNode("node-2")

	// Create mock clients, starting in a non-stopped state to ensure Stop() is called
	mockClient1 := mlnodeclient.NewMockClient()
	mockClient1.CurrentState = mlnodeclient.MlNodeState_INFERENCE
	mockClient2 := mlnodeclient.NewMockClient()
	mockClient2.CurrentState = mlnodeclient.MlNodeState_INFERENCE

	// Create node workers with mock clients
	worker1 := NewNodeWorkerWithClient("node-1", node1, mockClient1)
	worker2 := NewNodeWorkerWithClient("node-2", node2, mockClient2)

	// Create work group
	workGroup := NewNodeWorkGroup()
	workGroup.AddWorker("node-1", worker1)
	workGroup.AddWorker("node-2", worker2)

	// Create mock broker
	// Provide a mock CosmosMessageClient for GetAddress()
	broker := &Broker{
		nodes: map[string]*NodeWithState{
			"node-1": node1,
			"node-2": node2,
		},
		nodeWorkGroup: workGroup,
	}

	// Execute StartPocCommand
	cmd := StartPocCommand{
		BlockHeight: 12345,
		BlockHash:   "0xABCDEF",
		PubKey:      "test-pubkey",
		CallbackUrl: "http://callback.url",
		Response:    make(chan bool, 1),
	}

	cmd.Execute(broker)

	// Verify response
	success := <-cmd.Response
	assert.True(t, success, "Command should succeed")

	// Verify intended status was updated
	assert.Equal(t, types.HardwareNodeStatus_POC, node1.State.IntendedStatus)
	assert.Equal(t, types.HardwareNodeStatus_POC, node2.State.IntendedStatus)

	// Verify mock clients were called correctly
	assert.Equal(t, 1, mockClient1.StopCalled, "Stop should be called once on node1")
	assert.Equal(t, 1, mockClient1.InitGenerateCalled, "InitGenerate should be called once on node1")
	assert.Equal(t, 1, mockClient2.StopCalled, "Stop should be called once on node2")
	assert.Equal(t, 1, mockClient2.InitGenerateCalled, "InitGenerate should be called once on node2")

	// Verify InitDto parameters
	require.NotNil(t, mockClient1.LastInitDto)
	assert.Equal(t, int64(12345), mockClient1.LastInitDto.BlockHeight)
	assert.Equal(t, "0xABCDEF", mockClient1.LastInitDto.BlockHash)
	assert.Equal(t, "test-pubkey", mockClient1.LastInitDto.PublicKey)
	assert.Equal(t, "http://callback.url", mockClient1.LastInitDto.URL)
}

func TestStartPocCommand_AlreadyInPoC(t *testing.T) {
	// Create test node
	node := createTestNode("node-1")

	// Create mock client already in PoC state
	mockClient := mlnodeclient.NewMockClient()
	mockClient.CurrentState = mlnodeclient.MlNodeState_POW
	mockClient.PowStatus = mlnodeclient.POW_GENERATING

	// Create node worker with mock client
	worker := NewNodeWorkerWithClient("node-1", node, mockClient)

	// Create work group
	workGroup := NewNodeWorkGroup()
	workGroup.AddWorker("node-1", worker)

	// Create mock broker
	broker := &Broker{
		nodes: map[string]*NodeWithState{
			"node-1": node,
		},
		nodeWorkGroup: workGroup,
	}

	// Execute StartPocCommand
	cmd := StartPocCommand{
		BlockHeight: 12345,
		BlockHash:   "0xABCDEF",
		PubKey:      "test-pubkey",
		CallbackUrl: "http://callback.url",
		Response:    make(chan bool, 1),
	}

	cmd.Execute(broker)

	// Verify response
	success := <-cmd.Response
	assert.True(t, success, "Command should succeed")

	// Verify Stop and InitGenerate were NOT called (idempotency)
	assert.Equal(t, 0, mockClient.StopCalled, "Stop should not be called when already in PoC")
	assert.Equal(t, 0, mockClient.InitGenerateCalled, "InitGenerate should not be called when already in PoC")
}

func TestStartPocCommand_StopFails(t *testing.T) {
	// Create test node
	node := createTestNode("node-1")

	// Create mock client that fails on Stop, initially in a non-stopped state
	mockClient := mlnodeclient.NewMockClient()
	mockClient.CurrentState = mlnodeclient.MlNodeState_INFERENCE
	mockClient.StopError = errors.New("stop failed")

	// Create node worker with mock client
	worker := NewNodeWorkerWithClient("node-1", node, mockClient)

	// Create work group
	workGroup := NewNodeWorkGroup()
	workGroup.AddWorker("node-1", worker)

	// Create mock broker
	broker := &Broker{
		nodes: map[string]*NodeWithState{
			"node-1": node,
		},
		nodeWorkGroup: workGroup,
	}

	// Execute StartPocCommand
	cmd := StartPocCommand{
		BlockHeight: 12345,
		BlockHash:   "0xABCDEF",
		PubKey:      "test-pubkey",
		CallbackUrl: "http://callback.url",
		Response:    make(chan bool, 1),
	}

	cmd.Execute(broker)

	// Verify response
	success := <-cmd.Response
	assert.True(t, success, "Command should still report success (individual node failures don't block)")

	// Verify Stop was called but InitGenerate was not
	assert.Equal(t, 1, mockClient.StopCalled, "Stop should be called")
	assert.Equal(t, 0, mockClient.InitGenerateCalled, "InitGenerate should not be called after Stop failure")

	// Verify node state shows failure
	assert.Equal(t, "Failed to stop for PoC", node.State.FailureReason)
}
