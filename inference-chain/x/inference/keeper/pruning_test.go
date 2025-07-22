package keeper_test

import (
	"testing"

	"github.com/google/uuid"
	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func TestPruneInferences(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)

	// Set up test data
	currentEpochIndex := uint64(5)
	pruningThreshold := uint64(2)

	// Create inferences with different statuses and epochs
	// Inference 1: FINISHED, epoch 1 - should be pruned (epoch diff = 4 > threshold)
	inference1 := types.Inference{
		Index:       "inference1",
		InferenceId: uuid.New().String(),
		Status:      types.InferenceStatus_FINISHED,
		EpochId:     1,
	}

	// Inference 2: VALIDATED, epoch 2 - should be pruned (epoch diff = 3 > threshold)
	inference2 := types.Inference{
		Index:       "inference2",
		InferenceId: uuid.New().String(),
		Status:      types.InferenceStatus_VALIDATED,
		EpochId:     2,
	}

	// Inference 3: INVALIDATED, epoch 3 - should be pruned (epoch diff = 2 = threshold)
	inference3 := types.Inference{
		Index:       "inference3",
		InferenceId: uuid.New().String(),
		Status:      types.InferenceStatus_INVALIDATED,
		EpochId:     3,
	}

	// Inference 4: EXPIRED, epoch 4 - should not be pruned (epoch diff = 1 < threshold)
	inference4 := types.Inference{
		Index:       "inference4",
		InferenceId: uuid.New().String(),
		Status:      types.InferenceStatus_EXPIRED,
		EpochId:     4,
	}

	// Inference 5: STARTED, epoch 1 - should not be pruned (not terminal status)
	inference5 := types.Inference{
		Index:       "inference5",
		InferenceId: uuid.New().String(),
		Status:      types.InferenceStatus_STARTED,
		EpochId:     1,
	}

	// Store inferences
	k.SetInference(ctx, inference1)
	k.SetInference(ctx, inference2)
	k.SetInference(ctx, inference3)
	k.SetInference(ctx, inference4)
	k.SetInference(ctx, inference5)

	// Verify all inferences are stored
	inferences := k.GetAllInference(ctx)
	require.Len(t, inferences, 5)

	// Run pruning
	err := k.PruneInferences(ctx, currentEpochIndex, pruningThreshold)
	require.NoError(t, err)

	// Verify only the expected inferences remain
	remainingInferences := k.GetAllInference(ctx)
	require.Len(t, remainingInferences, 2)

	// Check that inference4 and inference5 remain
	var foundInference4, foundInference5 bool
	for _, inf := range remainingInferences {
		if inf.Index == "inference4" {
			foundInference4 = true
		}
		if inf.Index == "inference5" {
			foundInference5 = true
		}
	}
	require.True(t, foundInference4, "Inference4 should not be pruned")
	require.True(t, foundInference5, "Inference5 should not be pruned")

	// Check that inference1, inference2, and inference3 are pruned
	_, found1 := k.GetInference(ctx, "inference1")
	_, found2 := k.GetInference(ctx, "inference2")
	_, found3 := k.GetInference(ctx, "inference3")
	require.False(t, found1, "Inference1 should be pruned")
	require.False(t, found2, "Inference2 should be pruned")
	require.False(t, found3, "Inference3 should be pruned")
}

func TestPruneInferencesWithZeroThreshold(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)

	// Set up test data
	currentEpochIndex := uint64(5)
	pruningThreshold := uint64(0) // Disable pruning

	// Create an inference that would normally be pruned
	inference := types.Inference{
		Index:       "inference1",
		InferenceId: uuid.New().String(),
		Status:      types.InferenceStatus_FINISHED,
		EpochId:     1,
	}

	// Store inference
	k.SetInference(ctx, inference)

	// Verify inference is stored
	inferences := k.GetAllInference(ctx)
	require.Len(t, inferences, 1)

	// Run pruning with zero threshold
	err := k.PruneInferences(ctx, currentEpochIndex, pruningThreshold)
	require.NoError(t, err)

	// Verify inference still exists (pruning disabled)
	remainingInferences := k.GetAllInference(ctx)
	require.Len(t, remainingInferences, 1)
}

// Note: Testing PrunePoCData is more complex as it requires setting up epochs and PoC data.
// In a real implementation, you would need to create epochs and PoC data, but for simplicity,
// we'll focus on testing the inference pruning functionality in this test file.
