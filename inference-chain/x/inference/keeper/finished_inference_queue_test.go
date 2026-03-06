package keeper_test

import (
	"testing"

	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/stretchr/testify/require"
)

func TestPendingInferenceValidationQueue_ListFinishedInferenceIDs_FIFO(t *testing.T) {
	keeper, ctx := keepertest.InferenceKeeper(t)

	require.NoError(t, keeper.EnqueueFinishedInference(ctx, "c"))
	require.NoError(t, keeper.EnqueueFinishedInference(ctx, "a"))
	require.NoError(t, keeper.EnqueueFinishedInference(ctx, "b"))
	require.NoError(t, keeper.EnqueueFinishedInference(ctx, "other"))

	got, err := keeper.ListFinishedInferenceIDs(ctx)
	require.NoError(t, err)
	require.Equal(t, []string{"c", "a", "b", "other"}, got)

	// Queue is not reset on read
	got2, err := keeper.ListFinishedInferenceIDs(ctx)
	require.NoError(t, err)
	require.Equal(t, got, got2)
}

func TestFinishedInferenceQueue_Empty(t *testing.T) {
	keeper, ctx := keepertest.InferenceKeeper(t)

	got, err := keeper.ListFinishedInferenceIDs(ctx)
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestFinishedInferenceQueue_Single(t *testing.T) {
	keeper, ctx := keepertest.InferenceKeeper(t)

	require.NoError(t, keeper.EnqueueFinishedInference(ctx, "single"))

	got, err := keeper.ListFinishedInferenceIDs(ctx)
	require.NoError(t, err)
	require.Equal(t, []string{"single"}, got)
}

func TestFinishedInferenceQueue_Large(t *testing.T) {
	keeper, ctx := keepertest.InferenceKeeper(t)

	expected := make([]string, 100)
	for i := 0; i < 100; i++ {
		id := "id-" + string(rune(i))
		expected[i] = id
		require.NoError(t, keeper.EnqueueFinishedInference(ctx, id))
	}

	got, err := keeper.ListFinishedInferenceIDs(ctx)
	require.NoError(t, err)
	require.Equal(t, expected, got)
}

func TestFinishedInferenceQueue_EmptyID(t *testing.T) {
	keeper, ctx := keepertest.InferenceKeeper(t)

	// Currently, EnqueueFinishedInference allows empty IDs.
	// ListFinishedInferenceIDs skips empty byte slices.
	require.NoError(t, keeper.EnqueueFinishedInference(ctx, ""))
	require.NoError(t, keeper.EnqueueFinishedInference(ctx, "valid"))
	require.NoError(t, keeper.EnqueueFinishedInference(ctx, ""))

	got, err := keeper.ListFinishedInferenceIDs(ctx)
	require.NoError(t, err)
	// Based on the code:
	// if len(bz) == 0 { continue }
	// So empty strings are filtered out.
	require.Equal(t, []string{"valid"}, got)
}
