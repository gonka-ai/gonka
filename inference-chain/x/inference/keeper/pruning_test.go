package keeper_test

import (
	"fmt"
	"testing"

	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

// TestPruningBasic tests the basic functionality of the pruning system
func TestPruningBasic(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)

	// Create a test inference
	inference := types.Inference{
		Index:   "test-inference",
		EpochId: 1,
		Status:  types.InferenceStatus_FINISHED,
	}

	// Add inference to the store without calculating developer stats
	k.SetInferenceWithoutDevStatComputation(ctx, inference)

	// Verify inference exists
	_, found := k.GetInference(ctx, "test-inference")
	require.True(t, found, "Inference should exist before pruning")

	// Run pruning with a threshold that should prune the inference
	err := k.PruneInferences(ctx, 4, 2) // Current epoch 4, threshold 2
	require.NoError(t, err)

	// Verify inference was pruned
	_, found = k.GetInference(ctx, "test-inference")
	require.False(t, found, "Inference should be pruned")
}

// TestPruningEpochThreshold tests that only inferences older than the threshold are pruned
func TestPruningEpochThreshold(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)

	// Create inferences with different epoch IDs
	inferences := []types.Inference{
		{
			Index:   "inference-epoch1",
			EpochId: 1, // Old enough to be pruned
			Status:  types.InferenceStatus_FINISHED,
		},
		{
			Index:   "inference-epoch2",
			EpochId: 2, // Old enough to be pruned
			Status:  types.InferenceStatus_FINISHED,
		},
		{
			Index:   "inference-epoch3",
			EpochId: 3, // Not old enough to be pruned
			Status:  types.InferenceStatus_FINISHED,
		},
		{
			Index:   "inference-epoch4",
			EpochId: 4, // Current epoch, should not be pruned
			Status:  types.InferenceStatus_FINISHED,
		},
	}

	// Add inferences to the store without calculating developer stats
	for _, inf := range inferences {
		k.SetInferenceWithoutDevStatComputation(ctx, inf)
	}

	// Run pruning with threshold 2
	err := k.PruneInferences(ctx, 4, 2) // Current epoch 4, threshold 2
	require.NoError(t, err)

	// Verify correct inferences were pruned
	_, found := k.GetInference(ctx, "inference-epoch1")
	require.False(t, found, "Inference from epoch 1 should be pruned")

	_, found = k.GetInference(ctx, "inference-epoch2")
	require.False(t, found, "Inference from epoch 2 should be pruned")

	_, found = k.GetInference(ctx, "inference-epoch3")
	require.True(t, found, "Inference from epoch 3 should not be pruned")

	_, found = k.GetInference(ctx, "inference-epoch4")
	require.True(t, found, "Inference from epoch 4 should not be pruned")
}

// TestPruningStatusPreservation tests that inferences with VOTING and STARTED status are not pruned
func TestPruningStatusPreservation(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)

	// Create inferences with different statuses
	inferences := []types.Inference{
		{
			Index:   "inference-voting",
			EpochId: 1,
			Status:  types.InferenceStatus_VOTING,
		},
		{
			Index:   "inference-started",
			EpochId: 1,
			Status:  types.InferenceStatus_STARTED,
		},
		{
			Index:   "inference-finished",
			EpochId: 1,
			Status:  types.InferenceStatus_FINISHED,
		},
	}

	// Add inferences to the store
	for _, inf := range inferences {
		k.SetInferenceWithoutDevStatComputation(ctx, inf)
	}

	// Run pruning with threshold that should prune old inferences
	err := k.PruneInferences(ctx, 4, 2) // Current epoch 4, threshold 2
	require.NoError(t, err)

	// Verify VOTING inference was not pruned
	_, found := k.GetInference(ctx, "inference-voting")
	require.True(t, found, "Inference with VOTING status should not be pruned")

	// Verify STARTED inference was not pruned
	_, found = k.GetInference(ctx, "inference-started")
	require.True(t, found, "Inference with STARTED status should not be pruned")

	// Verify FINISHED inference was pruned
	_, found = k.GetInference(ctx, "inference-finished")
	require.False(t, found, "Inference with FINISHED status should be pruned")
}

// TestPruningMultipleEpochs tests pruning behavior over 10 epochs
func TestPruningMultipleEpochs(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)

	// Create inferences for 10 epochs
	inferences := []types.Inference{}
	for i := 1; i <= 10; i++ {
		inferences = append(inferences, types.Inference{
			Index:   fmt.Sprintf("inference-epoch%d", i),
			EpochId: uint64(i),
			Status:  types.InferenceStatus_FINISHED,
		})
	}

	// Add inferences to the store
	for _, inf := range inferences {
		k.SetInferenceWithoutDevStatComputation(ctx, inf)
	}

	// Run pruning with threshold 1 at epoch 10
	err := k.PruneInferences(ctx, 10, 1)
	require.NoError(t, err)

	// Verify inferences from epochs 1-7 are pruned
	for i := 1; i <= 9; i++ {
		_, found := k.GetInference(ctx, fmt.Sprintf("inference-epoch%d", i))
		require.False(t, found, fmt.Sprintf("Inference from epoch %d should be pruned", i))
	}

	// Verify inferences from epochs 8-10 are retained
	for i := 10; i <= 10; i++ {
		_, found := k.GetInference(ctx, fmt.Sprintf("inference-epoch%d", i))
		require.True(t, found, fmt.Sprintf("Inference from epoch %d should not be pruned", i))
	}

}
