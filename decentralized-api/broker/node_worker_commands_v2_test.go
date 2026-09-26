package broker

import (
	"context"
	"errors"
	"testing"

	"decentralized-api/chainphase"
	"decentralized-api/mlnodeclient"

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
	require.NotNil(t, mockClient.LastInitGenerateV2Req)
	assert.Equal(t, "http://localhost:8080/callback/test-model", mockClient.LastInitGenerateV2Req.URL)
}

// TestStartPoCNodeCommandV2_AlreadyGenerating verifies idempotency - if already generating, return success without restart.
func TestStartPoCNodeCommandV2_AlreadyGenerating(t *testing.T) {
	node := createTestNode("test-node-v2-gen")
	node.State.LastPocV2BlockHeight = 1000
	node.State.LastPocV2BlockHash = "test-block-hash"
	mockClient := mlnodeclient.NewMockClient()
	mockClient.SetV2Status("GENERATING")
	broker := NewTestBroker2(1)
	worker := NewNodeWorkerWithClient("test-node-v2-gen", node, mockClient, broker)
	defer worker.Shutdown()

	cmd := StartPoCNodeCommandV2{
		BlockHeight:          1000,
		BlockHash:            "test-block-hash",
		PubKey:               "test-pub-key",
		CallbackUrl:          "http://localhost:8080/callback",
		TotalNodes:           5,
		Model:                "test-model",
		SeqLen:               256,
		LastPocV2BlockHeight: 1000,
		LastPocV2BlockHash:   "test-block-hash",
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

func TestStartPoCNodeCommandV2_ChallengeWindDownStopsPow(t *testing.T) {
	node := createTestNode("test-node-v2-gen")
	node.State.LastPocV2BlockHeight = 500
	node.State.LastPocV2BlockHash = "challenge-hash"
	mockClient := mlnodeclient.NewMockClient()
	mockClient.SetV2Status("GENERATING")

	tracker := &chainphase.ChainPhaseTracker{}
	epoch := &types.Epoch{Index: 1, PocStartBlockHeight: 100}
	params := &types.EpochParams{
		EpochLength:           1000,
		EpochMultiplier:       1,
		PocStageDuration:      100,
		PocExchangeDuration:   50,
		PocValidationDelay:    10,
		PocValidationDuration: 100,
	}
	tracker.Update(chainphase.BlockInfo{Height: 897, Hash: "h"}, epoch, params, true, nil)
	withOverlay(t, stubChallengeOverlay{
		self: "me",
		ch: testOpenCh("me", 500, 900, true),
	})

	b := NewTestBroker2(1)
	b.phaseTracker = tracker
	worker := NewNodeWorkerWithClient("test-node-v2-gen", node, mockClient, b)
	defer worker.Shutdown()

	cmd := StartPoCNodeCommandV2{
		BlockHeight:          500,
		BlockHash:            "challenge-hash",
		PubKey:               "test-pub-key",
		CallbackUrl:          "http://localhost:8080/callback",
		TotalNodes:           1,
		Model:                "test-model",
		SeqLen:               256,
		WindDown:             true,
		LastPocV2BlockHeight: 500,
		LastPocV2BlockHash:   "challenge-hash",
	}
	result := cmd.Execute(context.Background(), worker)
	assert.True(t, result.Succeeded)
	assert.Equal(t, types.HardwareNodeStatus_POC, result.FinalStatus)
	assert.Equal(t, PocStatusGenerating, result.FinalPocStatus)

	mockClient.Mu.Lock()
	defer mockClient.Mu.Unlock()
	assert.Equal(t, 1, mockClient.StopPowV2Called, "wind-down should StopPowV2 via the node-worker command")
	assert.Equal(t, 0, mockClient.InitGenerateV2Called, "wind-down must not re-init generate")
}

func TestStartPoCNodeCommandV2_WindDownDoesNotInitOnStatusError(t *testing.T) {
	node := createTestNode("test-node-v2-gen")
	mockClient := mlnodeclient.NewMockClient()
	mockClient.GetPowStatusV2Error = errors.New("status down")
	b := NewTestBroker2(1)
	worker := NewNodeWorkerWithClient("test-node-v2-gen", node, mockClient, b)
	defer worker.Shutdown()

	cmd := StartPoCNodeCommandV2{WindDown: true, BlockHeight: 500, BlockHash: "h"}
	result := cmd.Execute(context.Background(), worker)
	assert.False(t, result.Succeeded)
	mockClient.Mu.Lock()
	defer mockClient.Mu.Unlock()
	assert.Equal(t, 0, mockClient.InitGenerateV2Called)
}

func TestStartPoCNodeCommandV2_ReinitWhenHeightHashChange(t *testing.T) {
	node := createTestNode("test-node-v2-gen")
	node.State.LastPocV2BlockHeight = 1000
	node.State.LastPocV2BlockHash = "old-hash"
	mockClient := mlnodeclient.NewMockClient()
	mockClient.SetV2Status("GENERATING")
	b := NewTestBroker2(1)
	worker := NewNodeWorkerWithClient("test-node-v2-gen", node, mockClient, b)
	defer worker.Shutdown()

	cmd := StartPoCNodeCommandV2{
		BlockHeight:          2000,
		BlockHash:            "new-hash",
		PubKey:               "test-pub-key",
		CallbackUrl:          "http://localhost:8080/callback",
		TotalNodes:           5,
		Model:                "test-model",
		SeqLen:               256,
		LastPocV2BlockHeight: 1000,
		LastPocV2BlockHash:   "old-hash",
	}

	result := cmd.Execute(context.Background(), worker)
	assert.True(t, result.Succeeded)
	assert.True(t, result.PocV2Updated)
	assert.Equal(t, int64(2000), result.PocV2BlockHeight)
	assert.Equal(t, "new-hash", result.PocV2BlockHash)
	mockClient.Mu.Lock()
	defer mockClient.Mu.Unlock()
	assert.Equal(t, 1, mockClient.StopPowV2Called, "must stop old segment before re-init")
	assert.Equal(t, 1, mockClient.InitGenerateV2Called, "InitGenerateV2 should re-init on height/hash change")
	require.NotNil(t, mockClient.LastInitGenerateV2Req)
	assert.Equal(t, int64(2000), mockClient.LastInitGenerateV2Req.BlockHeight)
	assert.Equal(t, "new-hash", mockClient.LastInitGenerateV2Req.BlockHash)
}

func TestStartPoCNodeCommandV2_UnknownLastTrustsGenerating(t *testing.T) {
	node := createTestNode("test-node-v2-gen")
	mockClient := mlnodeclient.NewMockClient()
	mockClient.SetV2Status("GENERATING")
	b := NewTestBroker2(1)
	worker := NewNodeWorkerWithClient("test-node-v2-gen", node, mockClient, b)
	defer worker.Shutdown()

	cmd := StartPoCNodeCommandV2{
		BlockHeight: 2000,
		BlockHash:   "new-hash",
		PubKey:      "test-pub-key",
		CallbackUrl: "http://localhost:8080/callback",
		TotalNodes:  5,
		Model:       "test-model",
		SeqLen:      256,
	}
	result := cmd.Execute(context.Background(), worker)
	assert.True(t, result.Succeeded)
	assert.True(t, result.PocV2Updated)
	assert.Equal(t, int64(2000), result.PocV2BlockHeight)
	mockClient.Mu.Lock()
	defer mockClient.Mu.Unlock()
	assert.Equal(t, 0, mockClient.StopPowV2Called)
	assert.Equal(t, 0, mockClient.InitGenerateV2Called)
}

// After a DAPI restart LastPocV2 is unknown, but the MLNode reports which
// stage it is generating. A stage left over from before the restart (e.g. the
// previous challenge segment) must be stopped and re-initialised, otherwise
// its callbacks are rejected as not the active stage and the new stage is empty.
func TestStartPoCNodeCommandV2_UnknownLastRestartsStaleStage(t *testing.T) {
	node := createTestNode("test-node-v2-gen")
	mockClient := mlnodeclient.NewMockClient()
	mockClient.SetV2Status("GENERATING")
	mockClient.PowStatusV2Config = &mlnodeclient.BackendPoCConfigV2{BlockHeight: 1000, BlockHash: "old-hash"}
	b := NewTestBroker2(1)
	worker := NewNodeWorkerWithClient("test-node-v2-gen", node, mockClient, b)
	defer worker.Shutdown()

	cmd := StartPoCNodeCommandV2{
		BlockHeight: 2000,
		BlockHash:   "new-hash",
		PubKey:      "test-pub-key",
		CallbackUrl: "http://localhost:8080/callback",
		TotalNodes:  5,
		Model:       "test-model",
		SeqLen:      256,
	}
	result := cmd.Execute(context.Background(), worker)
	assert.True(t, result.Succeeded)
	assert.Equal(t, int64(2000), result.PocV2BlockHeight)
	mockClient.Mu.Lock()
	defer mockClient.Mu.Unlock()
	assert.Equal(t, 1, mockClient.StopPowV2Called, "stale stage must be stopped")
	assert.Equal(t, 1, mockClient.InitGenerateV2Called, "requested stage must be initialised")
	require.NotNil(t, mockClient.LastInitGenerateV2Req)
	assert.Equal(t, int64(2000), mockClient.LastInitGenerateV2Req.BlockHeight)
}

func TestStartPoCNodeCommandV2_UnknownLastKeepsReportedSameStage(t *testing.T) {
	node := createTestNode("test-node-v2-gen")
	mockClient := mlnodeclient.NewMockClient()
	mockClient.SetV2Status("GENERATING")
	mockClient.PowStatusV2Config = &mlnodeclient.BackendPoCConfigV2{BlockHeight: 2000, BlockHash: "new-hash"}
	b := NewTestBroker2(1)
	worker := NewNodeWorkerWithClient("test-node-v2-gen", node, mockClient, b)
	defer worker.Shutdown()

	cmd := StartPoCNodeCommandV2{
		BlockHeight: 2000,
		BlockHash:   "new-hash",
		PubKey:      "test-pub-key",
		CallbackUrl: "http://localhost:8080/callback",
		TotalNodes:  5,
		Model:       "test-model",
		SeqLen:      256,
	}
	result := cmd.Execute(context.Background(), worker)
	assert.True(t, result.Succeeded)
	assert.True(t, result.PocV2Updated)
	mockClient.Mu.Lock()
	defer mockClient.Mu.Unlock()
	assert.Equal(t, 0, mockClient.StopPowV2Called)
	assert.Equal(t, 0, mockClient.InitGenerateV2Called)
}

func TestStartPoCNodeCommandV2_ValidatingSameParamsStopsThenInit(t *testing.T) {
	node := createTestNode("test-node-v2-gen")
	mockClient := mlnodeclient.NewMockClient()
	mockClient.SetV2Status("VALIDATING")
	b := NewTestBroker2(1)
	worker := NewNodeWorkerWithClient("test-node-v2-gen", node, mockClient, b)
	defer worker.Shutdown()

	cmd := StartPoCNodeCommandV2{
		BlockHeight:          500,
		BlockHash:            "challenge-hash",
		PubKey:               "test-pub-key",
		CallbackUrl:          "http://localhost:8080/callback",
		TotalNodes:           1,
		Model:                "test-model",
		SeqLen:               256,
		LastPocV2BlockHeight: 500,
		LastPocV2BlockHash:   "challenge-hash",
	}
	result := cmd.Execute(context.Background(), worker)
	assert.True(t, result.Succeeded)
	mockClient.Mu.Lock()
	defer mockClient.Mu.Unlock()
	assert.Equal(t, 1, mockClient.StopPowV2Called)
	assert.Equal(t, 1, mockClient.InitGenerateV2Called)
}

func TestStartPoCNodeCommandV2_EncodesCallbackModelID(t *testing.T) {
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
		Model:       "org/model-b",
		SeqLen:      256,
	}

	result := cmd.Execute(context.Background(), worker)

	assert.True(t, result.Succeeded)
	require.NotNil(t, mockClient.LastInitGenerateV2Req)
	assert.Equal(t, "http://localhost:8080/callback/org%252Fmodel-b", mockClient.LastInitGenerateV2Req.URL)
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

// TestStartPoCNodeCommandV2_StrongerRngPropagated verifies that PocStrongerRng is forwarded to InitGenerateV2.
func TestStartPoCNodeCommandV2_StrongerRngPropagated(t *testing.T) {
	node := createTestNode("test-node-rng")
	mockClient := mlnodeclient.NewMockClient()
	broker := NewTestBroker2(1)
	worker := NewNodeWorkerWithClient("test-node-rng", node, mockClient, broker)
	defer worker.Shutdown()

	cmd := StartPoCNodeCommandV2{
		BlockHeight:    1000,
		BlockHash:      "test-hash",
		PubKey:         "test-pub",
		CallbackUrl:    "http://localhost/cb",
		TotalNodes:     3,
		Model:          "test-model",
		SeqLen:         256,
		PocStrongerRng: true,
	}

	result := cmd.Execute(context.Background(), worker)

	require.True(t, result.Succeeded)

	mockClient.Mu.Lock()
	defer mockClient.Mu.Unlock()
	require.Equal(t, 1, mockClient.InitGenerateV2Called)
	require.NotNil(t, mockClient.LastInitGenerateV2Req)
	assert.True(t, mockClient.LastInitGenerateV2Req.PocStrongerRng, "PocStrongerRng must be forwarded to InitGenerateV2")
}
