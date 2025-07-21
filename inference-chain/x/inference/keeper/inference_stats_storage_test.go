package keeper_test

import (
	"testing"
	"time"

	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func TestInferenceStatsStorage(t *testing.T) {
	keeper, ctx := keepertest.InferenceKeeper(t)

	// Create a test inference
	inference := types.Inference{
		Index:                    "test-index",
		InferenceId:              "test-id",
		Status:                   types.InferenceStatus_FINISHED,
		StartBlockTimestamp:      time.Now().Add(-2 * time.Minute).UnixMilli(),
		EndBlockTimestamp:        time.Now().Add(-1 * time.Minute).UnixMilli(),
		EpochId:                  1,
		EpochPocStartBlockHeight: 100,
		PromptTokenCount:         10,
		CompletionTokenCount:     20,
		ActualCost:               1000,
		RequestedBy:              "developer1",
		ExecutedBy:               "executor1",
		TransferredBy:            "transferrer1",
		Model:                    "test-model",
		PromptPayload:            "This is a test prompt",
		ResponsePayload:          "This is a test response",
	}

	// Test SetInference creates both Inference and InferenceStatsStorage
	keeper.SetInference(ctx, inference)

	// Verify Inference was stored
	storedInference, found := keeper.GetInference(ctx, inference.Index)
	require.True(t, found)
	require.Equal(t, inference.InferenceId, storedInference.InferenceId)
	require.Equal(t, inference.PromptPayload, storedInference.PromptPayload)

	// Verify InferenceStatsStorage was also created
	statsStorage, found := keeper.GetInferenceStatsStorage(ctx, inference.Index)
	require.True(t, found)
	require.Equal(t, inference.InferenceId, statsStorage.InferenceId)

	// Test RemoveInference with convertToStats=true
	keeper.RemoveInference(ctx, inference.Index, true)

	// Verify full Inference is removed
	_, found = keeper.GetInferenceDirectFromStore(ctx, inference.Index)
	require.False(t, found)

	// Verify InferenceStatsStorage still exists
	statsStorage, found = keeper.GetInferenceStatsStorage(ctx, inference.Index)
	require.True(t, found)

	// Verify InferenceStatsStorage can be retrieved via GetInference
	statsInference, found := keeper.GetInference(ctx, inference.Index)
	require.True(t, found)
	require.Equal(t, inference.InferenceId, statsInference.InferenceId)
	require.Equal(t, inference.Status, statsInference.Status)
	require.Equal(t, inference.StartBlockTimestamp, statsInference.StartBlockTimestamp)
	require.Equal(t, inference.EndBlockTimestamp, statsInference.EndBlockTimestamp)
	require.Equal(t, inference.EpochId, statsInference.EpochId)
	require.Equal(t, inference.EpochPocStartBlockHeight, statsInference.EpochPocStartBlockHeight)
	require.Equal(t, inference.PromptTokenCount, statsInference.PromptTokenCount)
	require.Equal(t, inference.CompletionTokenCount, statsInference.CompletionTokenCount)
	require.Equal(t, inference.ActualCost, statsInference.ActualCost)
	require.Equal(t, inference.RequestedBy, statsInference.RequestedBy)
	require.Equal(t, inference.ExecutedBy, statsInference.ExecutedBy)
	require.Equal(t, inference.TransferredBy, statsInference.TransferredBy)
	require.Equal(t, inference.Model, statsInference.Model)

	// Verify payload fields are empty in the retrieved inference
	require.Empty(t, statsInference.PromptPayload)
	require.Empty(t, statsInference.ResponsePayload)
}
