package keeper_test

import (
	"fmt"
	"testing"

	"cosmossdk.io/collections"
	storetypes "cosmossdk.io/store/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

// Keeper.Prune has 11 pruners and the one that runs first is BlockHeight % 11:
// 0 is the inference index pruner, 7 is the epoch group validation pruner.

// pruneLikeEndBlock runs one Prune call on a cache branch that is written back even when
// Prune returns an error, which is what EndBlock does (it only logs the error).
func pruneLikeEndBlock(ctx sdk.Context, k keeper.Keeper, height, epoch int64) error {
	cms := ctx.MultiStore().CacheMultiStore()
	bctx := ctx.WithMultiStore(cms).WithBlockHeight(height).WithGasMeter(storetypes.NewInfiniteGasMeter())
	err := k.Prune(bctx, epoch)
	cms.Write()
	return err
}

// A pruner that fails while it is the first of the block must not stop the pruners behind it
// in that same block.
func TestPruneFirstPrunerFailingDoesNotSkipTheRest(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	require.NoError(t, k.PruningState.Set(ctx, types.PruningState{}))
	setPruningConfig(ctx, k, PruningSettings{InferenceThreshold: 2, InferenceMaxPrune: 5000})

	// A STARTED inference of epoch 1 with no effective epoch: its removal fails in every block.
	require.NoError(t, k.SetInference(ctx, types.Inference{Index: "stuck", EpochId: 1, Status: types.InferenceStatus_STARTED}))
	for i := 0; i < 10; i++ {
		require.NoError(t, k.EpochGroupValidationEntry.Set(ctx, collections.Join3(uint64(1), fmt.Sprintf("p%d", i), "inf")))
	}

	err := pruneLikeEndBlock(ctx, k, 11, 10) // 11 % 11 == 0: the inference pruner goes first
	require.ErrorContains(t, err, "cannot defer pruning inference")
	require.Zero(t, countEpochGroupValidations(t, k, ctx, 1), "the pruner behind the failing one ran in the same block")
	state, err := k.PruningState.Get(ctx)
	require.NoError(t, err)
	require.Zero(t, state.InferencePrunedEpoch, "the marker of the failing pruner did not move")
}

// The rotation decides who goes first and the shared budget caps the total in one block. At
// height 7 the epoch group validation pruner is first and takes its 100; the inference pruner
// then gets what is left of the 5000.
func TestPruneRotationAndSharedBudgetInOneBlock(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	require.NoError(t, k.PruningState.Set(ctx, types.PruningState{}))
	setPruningConfig(ctx, k, PruningSettings{InferenceThreshold: 2, InferenceMaxPrune: 5000})
	for i := 0; i < 6000; i++ {
		require.NoError(t, k.SetInference(ctx, types.Inference{Index: fmt.Sprintf("inf-%05d", i), EpochId: 1, Status: types.InferenceStatus_FINISHED}))
	}
	for i := 0; i < 100; i++ {
		require.NoError(t, k.EpochGroupValidationEntry.Set(ctx, collections.Join3(uint64(1), fmt.Sprintf("p%03d", i), "inf")))
	}
	require.Equal(t, 6000, countInferencesToPrune(t, k, ctx, 1))

	require.NoError(t, pruneLikeEndBlock(ctx, k, 7, 10))
	require.Zero(t, countEpochGroupValidations(t, k, ctx, 1), "the pruner that is first at height 7 takes its 100")
	require.Equal(t, 1100, countInferencesToPrune(t, k, ctx, 1), "the inference pruner gets the rest of the 5000: 4900")
}

// The epoch-0 pass takes its removals from the same per-block budget: when an earlier pruner
// has used all of it, the pass removes nothing in that block.
func TestPruneEpochZeroPassUsesTheSharedBudget(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	require.NoError(t, k.PruningState.Set(ctx, types.PruningState{}))
	setPruningConfig(ctx, k, PruningSettings{InferenceThreshold: 2, InferenceMaxPrune: 5000})
	for i := 0; i < 6000; i++ {
		require.NoError(t, k.SetInference(ctx, types.Inference{Index: fmt.Sprintf("inf-%05d", i), EpochId: 1, Status: types.InferenceStatus_FINISHED}))
	}
	const orphans = 300
	for i := 0; i < orphans; i++ {
		require.NoError(t, k.SetInference(ctx, types.Inference{Index: fmt.Sprintf("orphan-%05d", i), InferenceId: fmt.Sprintf("orphan-%05d", i), EpochId: 0, Status: types.InferenceStatus_EXPIRED}))
	}

	require.NoError(t, pruneLikeEndBlock(ctx, k, 11, 10)) // the inference pruner is first and uses all 5000
	require.Equal(t, 1000, countInferencesToPrune(t, k, ctx, 1))

	left := 0
	it, err := k.Inferences.Iterate(ctx, nil)
	require.NoError(t, err)
	defer it.Close()
	for ; it.Valid(); it.Next() {
		v, err := it.Value()
		require.NoError(t, err)
		if v.EpochId == 0 {
			left++
		}
	}
	require.Equal(t, orphans, left, "the epoch-0 pass ran outside the budget")
}
