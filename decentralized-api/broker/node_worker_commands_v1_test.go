package broker

import (
	"context"
	"testing"

	"decentralized-api/mlnodeclient"

	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/assert"
)

func TestStartPoCNodeCommandV1_Execute_AlreadyGenerating(t *testing.T) {
	mockClient := mlnodeclient.NewMockClient()
	mockClient.CurrentState = mlnodeclient.MlNodeState_POW
	mockClient.PowStatusV1 = mlnodeclient.PowStateV1Generating

	worker := &NodeWorker{
		nodeId: "test-node",
		node: &NodeWithState{
			Node:  Node{NodeNum: 1},
			State: NodeState{CurrentStatus: types.HardwareNodeStatus_POC},
		},
		getClientFn: func(node *NodeWithState) mlnodeclient.MLNodeClient {
			return mockClient
		},
	}

	cmd := StartPoCNodeCommandV1{
		BlockHeight: 100,
		BlockHash:   "hash",
		PubKey:      "pubkey",
		CallbackUrl: "http://callback",
		TotalNodes:  5,
	}

	result := cmd.Execute(context.Background(), worker)

	assert.True(t, result.Succeeded)
	assert.Equal(t, types.HardwareNodeStatus_POC, result.FinalStatus)
	assert.Equal(t, PocStatusGenerating, result.FinalPocStatus)
	// Should not have called InitGenerateV1 since already generating
	assert.Equal(t, 0, mockClient.InitGenerateV1Called)
}

func TestStartPoCNodeCommandV1_Execute_StopsAndStarts(t *testing.T) {
	mockClient := mlnodeclient.NewMockClient()
	mockClient.CurrentState = mlnodeclient.MlNodeState_INFERENCE

	worker := &NodeWorker{
		nodeId: "test-node",
		node: &NodeWithState{
			Node:  Node{NodeNum: 1},
			State: NodeState{CurrentStatus: types.HardwareNodeStatus_INFERENCE},
		},
		getClientFn: func(node *NodeWithState) mlnodeclient.MLNodeClient {
			return mockClient
		},
	}

	cmd := StartPoCNodeCommandV1{
		BlockHeight: 100,
		BlockHash:   "hash",
		PubKey:      "pubkey",
		CallbackUrl: "http://callback",
		TotalNodes:  5,
	}

	result := cmd.Execute(context.Background(), worker)

	assert.True(t, result.Succeeded)
	assert.Equal(t, types.HardwareNodeStatus_POC, result.FinalStatus)
	assert.Equal(t, PocStatusGenerating, result.FinalPocStatus)
	// Should have stopped node first (V1 requirement)
	assert.Equal(t, 1, mockClient.StopCalled)
	// Should have called InitGenerateV1
	assert.Equal(t, 1, mockClient.InitGenerateV1Called)
}

func TestStartPoCNodeCommandV1_Execute_StopError(t *testing.T) {
	mockClient := mlnodeclient.NewMockClient()
	mockClient.CurrentState = mlnodeclient.MlNodeState_INFERENCE
	mockClient.StopError = assert.AnError

	worker := &NodeWorker{
		nodeId: "test-node",
		node: &NodeWithState{
			Node:  Node{NodeNum: 1},
			State: NodeState{CurrentStatus: types.HardwareNodeStatus_INFERENCE},
		},
		getClientFn: func(node *NodeWithState) mlnodeclient.MLNodeClient {
			return mockClient
		},
	}

	cmd := StartPoCNodeCommandV1{
		BlockHeight: 100,
		BlockHash:   "hash",
		PubKey:      "pubkey",
		CallbackUrl: "http://callback",
		TotalNodes:  5,
	}

	result := cmd.Execute(context.Background(), worker)

	assert.False(t, result.Succeeded)
	assert.Equal(t, types.HardwareNodeStatus_FAILED, result.FinalStatus)
	assert.Contains(t, result.Error, "assert.AnError")
}

func TestInitValidateNodeCommandV1_Execute_AlreadyValidating(t *testing.T) {
	mockClient := mlnodeclient.NewMockClient()
	mockClient.CurrentState = mlnodeclient.MlNodeState_POW
	mockClient.PowStatusV1 = mlnodeclient.PowStateV1Validating

	worker := &NodeWorker{
		nodeId: "test-node",
		node: &NodeWithState{
			Node:  Node{NodeNum: 1},
			State: NodeState{CurrentStatus: types.HardwareNodeStatus_POC},
		},
		getClientFn: func(node *NodeWithState) mlnodeclient.MLNodeClient {
			return mockClient
		},
	}

	cmd := InitValidateNodeCommandV1{
		BlockHeight: 100,
		BlockHash:   "hash",
		PubKey:      "pubkey",
		CallbackUrl: "http://callback",
		TotalNodes:  5,
	}

	result := cmd.Execute(context.Background(), worker)

	assert.True(t, result.Succeeded)
	assert.Equal(t, types.HardwareNodeStatus_POC, result.FinalStatus)
	assert.Equal(t, PocStatusValidating, result.FinalPocStatus)
	// Should not have called InitValidateV1 since already validating
	assert.Equal(t, 0, mockClient.InitValidateV1Called)
}

func TestInitValidateNodeCommandV1_Execute_TransitionsFromPOW(t *testing.T) {
	mockClient := mlnodeclient.NewMockClient()
	mockClient.CurrentState = mlnodeclient.MlNodeState_POW
	mockClient.PowStatusV1 = mlnodeclient.PowStateV1Generating

	worker := &NodeWorker{
		nodeId: "test-node",
		node: &NodeWithState{
			Node:  Node{NodeNum: 1},
			State: NodeState{CurrentStatus: types.HardwareNodeStatus_POC},
		},
		getClientFn: func(node *NodeWithState) mlnodeclient.MLNodeClient {
			return mockClient
		},
	}

	cmd := InitValidateNodeCommandV1{
		BlockHeight: 100,
		BlockHash:   "hash",
		PubKey:      "pubkey",
		CallbackUrl: "http://callback",
		TotalNodes:  5,
	}

	result := cmd.Execute(context.Background(), worker)

	assert.True(t, result.Succeeded)
	assert.Equal(t, types.HardwareNodeStatus_POC, result.FinalStatus)
	assert.Equal(t, PocStatusValidating, result.FinalPocStatus)
	// Should NOT stop when in POW state (V1 can transition from generating to validating)
	assert.Equal(t, 0, mockClient.StopCalled)
	assert.Equal(t, 1, mockClient.InitValidateV1Called)
}

func TestInitValidateNodeCommandV1_Execute_StopsFromInference(t *testing.T) {
	mockClient := mlnodeclient.NewMockClient()
	mockClient.CurrentState = mlnodeclient.MlNodeState_INFERENCE

	worker := &NodeWorker{
		nodeId: "test-node",
		node: &NodeWithState{
			Node:  Node{NodeNum: 1},
			State: NodeState{CurrentStatus: types.HardwareNodeStatus_INFERENCE},
		},
		getClientFn: func(node *NodeWithState) mlnodeclient.MLNodeClient {
			return mockClient
		},
	}

	cmd := InitValidateNodeCommandV1{
		BlockHeight: 100,
		BlockHash:   "hash",
		PubKey:      "pubkey",
		CallbackUrl: "http://callback",
		TotalNodes:  5,
	}

	result := cmd.Execute(context.Background(), worker)

	assert.True(t, result.Succeeded)
	assert.Equal(t, types.HardwareNodeStatus_POC, result.FinalStatus)
	assert.Equal(t, PocStatusValidating, result.FinalPocStatus)
	// Should stop when transitioning from INFERENCE (not POW)
	assert.Equal(t, 1, mockClient.StopCalled)
	assert.Equal(t, 1, mockClient.InitValidateV1Called)
}

func TestInitValidateNodeCommandV1_Execute_CancelledContext(t *testing.T) {
	mockClient := mlnodeclient.NewMockClient()

	worker := &NodeWorker{
		nodeId: "test-node",
		node: &NodeWithState{
			Node:  Node{NodeNum: 1},
			State: NodeState{CurrentStatus: types.HardwareNodeStatus_POC},
		},
		getClientFn: func(node *NodeWithState) mlnodeclient.MLNodeClient {
			return mockClient
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	cmd := InitValidateNodeCommandV1{
		BlockHeight: 100,
	}

	result := cmd.Execute(ctx, worker)

	assert.False(t, result.Succeeded)
	assert.Contains(t, result.Error, "context canceled")
}
