package keeper_test

import (
	"context"
	"fmt"
	"testing"

	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func setDeveloperStatsForPruning(t *testing.T, ctx context.Context, k keeper.Keeper, count int) {
	t.Helper()
	for i := 0; i < count; i++ {
		require.NoError(t, k.SetDeveloperStats(ctx, types.Inference{
			InferenceId:         fmt.Sprintf("inference-%04d", i),
			EpochId:             1,
			RequestedBy:         "developer",
			Model:               "model",
			StartBlockTimestamp: int64(i + 1),
			Status:              types.InferenceStatus_FINISHED,
		}))
	}
}

func TestDeveloperStatsPruningSharesPerBlockBudgetAndCompletes(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	const inferenceCount = 400
	setDeveloperStatsForPruning(t, ctx, k, inferenceCount)

	// The first call crosses prefix boundaries: 400 model keys, 400 inference
	// keys, and 200 time keys. The cap remains global across all prefixes.
	pruned, err := k.PruneDeveloperStats(ctx)
	require.NoError(t, err)
	require.Equal(t, keeper.DeveloperStatsPruningMaxPerBlock, pruned)

	totalPruned := pruned
	for {
		pruned, err = k.PruneDeveloperStats(ctx)
		require.NoError(t, err)
		totalPruned += pruned
		if pruned == 0 {
			break
		}
	}

	// Three per-inference indexes plus one shared developer/epoch record.
	require.Equal(t, int64(3*inferenceCount+1), totalPruned)
	require.Empty(t, k.GetDeveloperStatsByTime(ctx, "developer", 0, inferenceCount+1))
	_, found := k.GetDevelopersStatsByEpoch(ctx, "developer", 1)
	require.False(t, found)
}

func TestDeveloperStatsByEpochPruningDeletesOneLargeRecordPerBlock(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)

	for i := 1; i <= 3; i++ {
		require.NoError(t, k.SetDeveloperStats(ctx, types.Inference{
			InferenceId:         fmt.Sprintf("inference-%d", i),
			EpochId:             uint64(i),
			RequestedBy:         "developer",
			Model:               "model",
			StartBlockTimestamp: int64(i),
			Status:              types.InferenceStatus_FINISHED,
		}))
	}

	// The nine per-inference index entries are removed in the first call, but
	// only one of the three aggregate epoch records is removed with them.
	pruned, err := k.PruneDeveloperStats(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(10), pruned)

	pruned, err = k.PruneDeveloperStats(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(1), pruned)

	pruned, err = k.PruneDeveloperStats(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(1), pruned)

	pruned, err = k.PruneDeveloperStats(ctx)
	require.NoError(t, err)
	require.Zero(t, pruned)
}

func TestDeveloperStatsPruningRunsFromKeeperPrune(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	require.NoError(t, k.PruningState.Set(ctx, types.PruningState{}))
	setDeveloperStatsForPruning(t, ctx, k, 1)

	require.NoError(t, k.Prune(ctx, 0))
	require.Empty(t, k.GetDeveloperStatsByTime(ctx, "developer", 0, 2))
	_, found := k.GetDevelopersStatsByEpoch(ctx, "developer", 1)
	require.False(t, found)
}
