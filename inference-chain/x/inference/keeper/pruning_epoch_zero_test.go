package keeper_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func TestPruneEpochZeroInferences(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	require.NoError(t, k.PruningState.Set(ctx, types.PruningState{}))
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	threshold := int64(params.EpochParams.InferencePruningEpochThreshold)
	require.Positive(t, threshold)

	set := func(id string, epoch uint64, status types.InferenceStatus) {
		require.NoError(t, k.SetInference(ctx, types.Inference{Index: id, InferenceId: id, EpochId: epoch, Status: status}))
	}
	const orphans = 2*keeper.EpochZeroInferencePruningMaxPerBlock + 500
	for i := 0; i < orphans; i++ {
		status := types.InferenceStatus_EXPIRED // started, never finished
		if i%3 == 0 {
			status = types.InferenceStatus_FINISHED // finished, never started
		}
		set(fmt.Sprintf("orphan-%05d", i), 0, status)
	}
	set("started-0", 0, types.InferenceStatus_STARTED)
	set("voting-0", 0, types.InferenceStatus_VOTING)
	current := threshold + 1
	set("indexed-current", uint64(current), types.InferenceStatus_FINISHED)

	count := func() (all int, epochZero int) {
		it, err := k.Inferences.Iterate(ctx, nil)
		require.NoError(t, err)
		defer it.Close()
		for ; it.Valid(); it.Next() {
			v, err := it.Value()
			require.NoError(t, err)
			all++
			if v.EpochId == 0 {
				epochZero++
			}
		}
		return
	}

	// Epoch 0 is not yet past the threshold: nothing is touched.
	require.NoError(t, k.Prune(ctx, threshold-1))
	_, zero := count()
	require.Equal(t, orphans+2, zero)

	require.NoError(t, k.Prune(ctx, current))
	_, zero = count()
	require.Equal(t, orphans+2-keeper.EpochZeroInferencePruningMaxPerBlock, zero, "one block removes at most the per-block budget")
	state, err := k.PruningState.Get(ctx)
	require.NoError(t, err)
	require.False(t, state.EpochZeroInferencesPruned)
	require.NotEmpty(t, state.EpochZeroInferencesCursor)

	for i := 0; i < 5; i++ {
		require.NoError(t, k.Prune(ctx, current))
	}
	all, zero := count()
	require.Equal(t, 2, zero, "only the STARTED and VOTING epoch-0 inferences stay")
	require.Equal(t, 3, all)
	_, found := k.GetInference(ctx, "started-0")
	require.True(t, found)
	_, found = k.GetInference(ctx, "voting-0")
	require.True(t, found)
	_, found = k.GetInference(ctx, "indexed-current")
	require.True(t, found, "inferences with an epoch are left to the indexed pruner")
	state, err = k.PruningState.Get(ctx)
	require.NoError(t, err)
	require.True(t, state.EpochZeroInferencesPruned)
	require.Empty(t, state.EpochZeroInferencesCursor)

	// Once done, the pass does not walk the store again.
	set("orphan-late", 0, types.InferenceStatus_EXPIRED)
	require.NoError(t, k.Prune(ctx, current))
	_, found = k.GetInference(ctx, "orphan-late")
	require.True(t, found)
}

func TestPruneEpochZeroInferencesScanBound(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	require.NoError(t, k.PruningState.Set(ctx, types.PruningState{}))
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	current := int64(params.EpochParams.InferencePruningEpochThreshold) + 1

	// Keys an epoch-0 pass must read but not remove; they sort before the orphan.
	for i := 0; i < keeper.EpochZeroInferenceScanMaxPerBlock; i++ {
		id := fmt.Sprintf("a-started-%05d", i)
		require.NoError(t, k.SetInference(ctx, types.Inference{Index: id, Status: types.InferenceStatus_STARTED}))
	}
	require.NoError(t, k.SetInference(ctx, types.Inference{Index: "z-orphan", Status: types.InferenceStatus_EXPIRED}))

	require.NoError(t, k.Prune(ctx, current))
	_, found := k.GetInference(ctx, "z-orphan")
	require.True(t, found, "the block stops at the scan budget")
	require.NoError(t, k.Prune(ctx, current))
	_, found = k.GetInference(ctx, "z-orphan")
	require.False(t, found)
	state, err := k.PruningState.Get(ctx)
	require.NoError(t, err)
	require.True(t, state.EpochZeroInferencesPruned)
}

// A removal failing mid-pass leaves the cursor where it was, so the next block scans the
// same range again and nothing is skipped.
func TestPruneEpochZeroInferencesFailureKeepsCursor(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	require.NoError(t, k.PruningState.Set(ctx, types.PruningState{}))
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	current := int64(params.EpochParams.InferencePruningEpochThreshold) + 1
	for i := 0; i < 10; i++ {
		id := fmt.Sprintf("orphan-%02d", i)
		require.NoError(t, k.SetInference(ctx, types.Inference{Index: id, Status: types.InferenceStatus_EXPIRED}))
	}

	removed := 0
	failAfter := func(ctx context.Context, id string) error {
		if removed == 4 {
			return errors.New("injected")
		}
		removed++
		return k.Inferences.Remove(ctx, id)
	}
	require.Error(t, keeper.PruneEpochZeroInferencesForTesting(k, ctx, current, failAfter))
	state, err := k.PruningState.Get(ctx)
	require.NoError(t, err)
	require.Empty(t, state.EpochZeroInferencesCursor)
	require.False(t, state.EpochZeroInferencesPruned)

	require.NoError(t, keeper.PruneEpochZeroInferencesForTesting(k, ctx, current, nil))
	for i := 0; i < 10; i++ {
		_, found := k.GetInference(ctx, fmt.Sprintf("orphan-%02d", i))
		require.False(t, found, "orphan-%02d", i)
	}
	state, err = k.PruningState.Get(ctx)
	require.NoError(t, err)
	require.True(t, state.EpochZeroInferencesPruned)
}
