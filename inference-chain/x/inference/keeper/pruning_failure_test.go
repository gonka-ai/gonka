package keeper_test

import (
	"context"
	"errors"
	"testing"

	"cosmossdk.io/collections"
	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

// Same case as TestPrunerStopsAfterPartialFailure in #1773: a removal failing mid-epoch
// stops the pruner, the epoch is not marked and no later epoch is touched.
func TestPrunerStopsAfterRemovalFailure(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	require.NoError(t, k.PruningState.Set(ctx, types.PruningState{}))

	for _, key := range []collections.Pair[int64, string]{
		collections.Join(int64(1), "a"),
		collections.Join(int64(1), "b"),
		collections.Join(int64(1), "c"),
		collections.Join(int64(2), "later-epoch"),
	} {
		require.NoError(t, k.InferencesToPrune.Set(ctx, key, collections.NoValue{}))
	}

	errInjected := errors.New("injected removal failure")
	removeCalls := 0
	pruner := keeper.Pruner[collections.Pair[int64, string], collections.NoValue]{
		Threshold:  1,
		PruningMax: 3,
		List:       k.InferencesToPrune,
		Ranger: func(_ context.Context, epoch int64) collections.Ranger[collections.Pair[int64, string]] {
			return collections.NewPrefixedPairRange[int64, string](epoch)
		},
		GetLastPruned: func(state types.PruningState) int64 { return state.InferencePrunedEpoch },
		SetLastPruned: func(state *types.PruningState, epoch int64) { state.InferencePrunedEpoch = epoch },
		Remover: func(ctx context.Context, key collections.Pair[int64, string]) error {
			removeCalls++
			if removeCalls == 3 {
				return errInjected
			}
			return k.InferencesToPrune.Remove(ctx, key)
		},
		Logger: k,
	}

	err := pruner.Prune(ctx, k, 3)
	require.ErrorIs(t, err, errInjected)
	require.Equal(t, 3, removeCalls)

	state, err := k.PruningState.Get(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(0), state.InferencePrunedEpoch)
	hasLater, err := k.InferencesToPrune.Has(ctx, collections.Join(int64(2), "later-epoch"))
	require.NoError(t, err)
	require.True(t, hasLater)
}
