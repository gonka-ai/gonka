package keeper_test

import (
	"context"
	"fmt"
	"testing"

	"cosmossdk.io/collections"
	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func countInferencesToPrune(t *testing.T, k keeper.Keeper, ctx context.Context, epochs uint64) (n int) {
	for e := uint64(1); e <= epochs; e++ {
		it, err := k.InferencesToPrune.Iterate(ctx, collections.NewPrefixedPairRange[int64, string](int64(e)))
		require.NoError(t, err)
		for ; it.Valid(); it.Next() {
			n++
		}
		it.Close()
	}
	return n
}

func countEpochGroupValidations(t *testing.T, k keeper.Keeper, ctx context.Context, epochs uint64) (n int) {
	for e := uint64(1); e <= epochs; e++ {
		it, err := k.EpochGroupValidationEntry.Iterate(ctx, collections.NewPrefixedTripleRange[uint64, string, string](e))
		require.NoError(t, err)
		for ; it.Valid(); it.Next() {
			n++
		}
		it.Close()
	}
	return n
}

// Two lists catch up at once: weighted removals per block stay within the shared
// budget, both lists make progress, and both backlogs are eventually cleared.
func TestPruneSharedBudgetPerBlock(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	require.NoError(t, k.PruningState.Set(ctx, types.PruningState{}))
	setPruningConfig(ctx, k, PruningSettings{InferenceThreshold: 2, InferenceMaxPrune: 2000})

	const current = 10
	const epochs = 5
	for e := uint64(1); e <= epochs; e++ {
		for i := 0; i < 600; i++ {
			k.SetInference(ctx, types.Inference{Index: fmt.Sprintf("inf-%d-%d", e, i), EpochId: e, Status: types.InferenceStatus_FINISHED})
			require.NoError(t, k.EpochGroupValidationEntry.Set(ctx, collections.Join3(e, fmt.Sprintf("p%d", i), "inf")))
		}
	}

	inf, egv := countInferencesToPrune(t, k, ctx, epochs), countEpochGroupValidations(t, k, ctx, epochs)
	for block := int64(1); block <= 40 && inf+egv > 0; block++ {
		require.NoError(t, k.Prune(ctx.WithBlockHeight(block), current))
		inf2, egv2 := countInferencesToPrune(t, k, ctx, epochs), countEpochGroupValidations(t, k, ctx, epochs)
		work := int64(inf-inf2)*keeper.InferenceRemoveCost + int64(egv-egv2)
		require.LessOrEqual(t, work, keeper.PruneWorkPerBlock, "block %d", block)
		if block == 1 {
			require.Positive(t, work)
		}
		inf, egv = inf2, egv2
	}
	require.Zero(t, inf+egv)
}

// A pruner that keeps failing does not stop the pruners after it.
func TestPruneFailingPrunerDoesNotStopOthers(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	require.NoError(t, k.PruningState.Set(ctx, types.PruningState{}))
	setPruningConfig(ctx, k, PruningSettings{InferenceThreshold: 2, InferenceMaxPrune: 1000})

	// A STARTED inference is deferred to the effective epoch; with none set, its removal fails.
	k.SetInference(ctx, types.Inference{Index: "stuck", EpochId: 1, Status: types.InferenceStatus_STARTED})
	for i := 0; i < 10; i++ {
		require.NoError(t, k.EpochGroupValidationEntry.Set(ctx, collections.Join3(uint64(1), fmt.Sprintf("p%d", i), "inf")))
	}

	for block := int64(0); block < 10; block++ {
		require.Error(t, k.Prune(ctx.WithBlockHeight(block), 10))
	}
	require.Equal(t, 1, countInferencesToPrune(t, k, ctx, 1))
	require.Zero(t, countEpochGroupValidations(t, k, ctx, 1))
	state, err := k.PruningState.Get(ctx)
	require.NoError(t, err)
	require.Zero(t, state.InferencePrunedEpoch)
}
