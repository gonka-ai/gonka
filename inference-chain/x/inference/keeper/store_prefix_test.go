package keeper_test

import (
	"testing"

	"cosmossdk.io/collections"
	"github.com/stretchr/testify/require"

	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/keeper"
)

func TestClearStorePrefix_RemovesOnlyThatPrefix(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	cleared, kept := collections.NewPrefix(250), collections.NewPrefix(251)
	inCleared := [][]byte{append(cleared.Bytes(), 'a'), append(cleared.Bytes(), 'b', 'c')}
	inKept := append(kept.Bytes(), 'a')
	for _, key := range append(inCleared, inKept) {
		require.NoError(t, keeper.SetRawStoreBytesForTesting(k, ctx, key, []byte("v")))
	}

	removed, err := k.ClearStorePrefix(ctx, cleared)
	require.NoError(t, err)

	require.Equal(t, len(inCleared), removed)
	for _, key := range inCleared {
		has, err := keeper.HasRawStoreKeyForTesting(k, ctx, key)
		require.NoError(t, err)
		require.False(t, has)
	}
	has, err := keeper.HasRawStoreKeyForTesting(k, ctx, inKept)
	require.NoError(t, err)
	require.True(t, has)
}
