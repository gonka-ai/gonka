package broker

import (
	"context"
	"decentralized-api/mlnodeclient"
	"testing"

	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTransitionPoCToValidatingCommandV2_Success verifies that the v2 validation
// transition command returns the correct state without making any network calls.
func TestTransitionPoCToValidatingCommandV2_Success(t *testing.T) {
	node := createTestNodeWithStatus("test-node-v2", types.HardwareNodeStatus_POC)
	node.State.PocCurrentStatus = PocStatusGenerating
	mockClient := mlnodeclient.NewMockClient()
	broker := NewTestBroker2(1)
	worker := NewNodeWorkerWithClient("test-node-v2", node, mockClient, broker)
	defer worker.Shutdown()

	cmd := TransitionPoCToValidatingCommandV2{}
	result := cmd.Execute(context.Background(), worker)

	// Verify the command succeeds
	assert.True(t, result.Succeeded, "TransitionPoCToValidatingCommandV2 should succeed")
	assert.Equal(t, types.HardwareNodeStatus_POC, result.FinalStatus, "FinalStatus should be POC")
	assert.Equal(t, PocStatusValidating, result.FinalPocStatus, "FinalPocStatus should be Validating")
	assert.Equal(t, types.HardwareNodeStatus_POC, result.OriginalTarget, "OriginalTarget should be POC")
	assert.Equal(t, PocStatusValidating, result.OriginalPocTarget, "OriginalPocTarget should be Validating")

	// Verify NO network calls were made - this is the key difference from v1
	mockClient.Mu.Lock()
	defer mockClient.Mu.Unlock()
	assert.Equal(t, 0, mockClient.StopCalled, "Stop() should not be called")
}

// TestTransitionPoCToValidatingCommandV2_RejectsFailedNode verifies that the command
// rejects transitions when node is in FAILED state.
func TestTransitionPoCToValidatingCommandV2_RejectsFailedNode(t *testing.T) {
	node := createTestNodeWithStatus("test-node-v2-failed", types.HardwareNodeStatus_FAILED)
	mockClient := mlnodeclient.NewMockClient()
	broker := NewTestBroker2(1)
	worker := NewNodeWorkerWithClient("test-node-v2-failed", node, mockClient, broker)
	defer worker.Shutdown()

	cmd := TransitionPoCToValidatingCommandV2{}
	result := cmd.Execute(context.Background(), worker)

	assert.False(t, result.Succeeded, "Command should fail for FAILED node")
	assert.Contains(t, result.Error, "FAILED", "Error should mention FAILED state")
	assert.Equal(t, types.HardwareNodeStatus_FAILED, result.FinalStatus, "FinalStatus should remain FAILED")
}

// TestTransitionPoCToValidatingCommandV2_CancelledContext verifies that the command
// respects context cancellation.
func TestTransitionPoCToValidatingCommandV2_CancelledContext(t *testing.T) {
	node := createTestNode("test-node-v2")
	mockClient := mlnodeclient.NewMockClient()
	broker := NewTestBroker2(1)
	worker := NewNodeWorkerWithClient("test-node-v2", node, mockClient, broker)
	defer worker.Shutdown()

	// Set node's current status to POC/Generating
	node.State.CurrentStatus = types.HardwareNodeStatus_POC
	node.State.PocCurrentStatus = PocStatusGenerating

	// Create a cancelled context
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	cmd := TransitionPoCToValidatingCommandV2{}
	result := cmd.Execute(ctx, worker)

	// Verify the command fails due to cancelled context
	assert.False(t, result.Succeeded, "Command should fail when context is cancelled")
	assert.Contains(t, result.Error, "context canceled", "Error should mention context cancellation")
	// Status should remain unchanged
	assert.Equal(t, types.HardwareNodeStatus_POC, result.FinalStatus, "FinalStatus should remain POC")
	assert.Equal(t, PocStatusGenerating, result.FinalPocStatus, "FinalPocStatus should remain Generating")
}

// TestStartPoCNodeCommandV2_Success verifies that v2 generation checks status and calls InitGenerateV2.
func TestStartPoCNodeCommandV2_Success(t *testing.T) {
	node := createTestNode("test-node-v2-gen")
	mockClient := mlnodeclient.NewMockClient()
	broker := NewTestBroker2(1)
	worker := NewNodeWorkerWithClient("test-node-v2-gen", node, mockClient, broker)
	defer worker.Shutdown()

	cmd := StartPoCNodeCommandV2{
		BlockHeight: 1000,
		BlockHash:   "test-block-hash",
		PubKey:      "test-pub-key",
		CallbackUrl: "http://localhost:8080/callback",
		TotalNodes:  5,
		Model:       "test-model",
		SeqLen:      256,
	}

	result := cmd.Execute(context.Background(), worker)

	assert.True(t, result.Succeeded, "StartPoCNodeCommandV2 should succeed")
	assert.Equal(t, types.HardwareNodeStatus_POC, result.FinalStatus, "FinalStatus should be POC")
	assert.Equal(t, PocStatusGenerating, result.FinalPocStatus, "FinalPocStatus should be Generating")

	mockClient.Mu.Lock()
	defer mockClient.Mu.Unlock()
	assert.Equal(t, 0, mockClient.StopCalled, "Stop() should NOT be called")
	assert.Equal(t, 1, mockClient.GetPowStatusV2Called, "GetPowStatusV2() should be called for idempotency check")
	assert.Equal(t, 0, mockClient.StopPowV2Called, "StopPowV2() should NOT be called (status is IDLE)")
	assert.Equal(t, 1, mockClient.InitGenerateV2Called, "InitGenerateV2() should be called once")
}

// TestStartPoCNodeCommandV2_AlreadyGenerating verifies idempotency - if already generating, return success without restart.
func TestStartPoCNodeCommandV2_AlreadyGenerating(t *testing.T) {
	node := createTestNode("test-node-v2-gen")
	mockClient := mlnodeclient.NewMockClient()
	mockClient.SetV2Status("GENERATING")
	broker := NewTestBroker2(1)
	worker := NewNodeWorkerWithClient("test-node-v2-gen", node, mockClient, broker)
	defer worker.Shutdown()

	cmd := StartPoCNodeCommandV2{
		BlockHeight: 1000,
		BlockHash:   "test-block-hash",
		PubKey:      "test-pub-key",
		CallbackUrl: "http://localhost:8080/callback",
		TotalNodes:  5,
		Model:       "test-model",
		SeqLen:      256,
	}

	result := cmd.Execute(context.Background(), worker)

	assert.True(t, result.Succeeded, "StartPoCNodeCommandV2 should succeed (idempotent)")
	assert.Equal(t, types.HardwareNodeStatus_POC, result.FinalStatus, "FinalStatus should be POC")
	assert.Equal(t, PocStatusGenerating, result.FinalPocStatus, "FinalPocStatus should be Generating")

	mockClient.Mu.Lock()
	defer mockClient.Mu.Unlock()
	assert.Equal(t, 1, mockClient.GetPowStatusV2Called, "GetPowStatusV2() should be called for idempotency check")
	assert.Equal(t, 0, mockClient.StopPowV2Called, "StopPowV2() should NOT be called")
	assert.Equal(t, 0, mockClient.InitGenerateV2Called, "InitGenerateV2() should NOT be called (already generating)")
}

// TestStopPowV2_MockBehavior verifies the mock StopPowV2 works correctly.
func TestStopPowV2_MockBehavior(t *testing.T) {
	mockClient := mlnodeclient.NewMockClient()

	resp, err := mockClient.StopPowV2(context.Background())

	require.NoError(t, err, "StopPowV2 should not return error")
	require.NotNil(t, resp, "StopPowV2 should return response")
	assert.Equal(t, "OK", resp.Status, "Status should be OK")
	assert.Len(t, resp.Results, 1, "Should have one backend result")
	assert.Equal(t, "stopped", resp.Results[0].Status, "Backend status should be stopped")
}
