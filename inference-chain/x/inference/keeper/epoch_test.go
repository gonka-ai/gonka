package keeper_test

import (
	"context"
	"testing"

	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/stretchr/testify/require"
)

func TestSetEffectiveEpochIndex_SyncsBLSSigningEpoch(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)

	err := k.SetEffectiveEpochIndex(ctx, 7)
	require.NoError(t, err)

	effective, found := k.GetEffectiveEpochIndex(ctx)
	require.True(t, found)
	require.Equal(t, uint64(7), effective)

	signingEpoch, found := k.BlsKeeper.GetCurrentSigningEpochID(ctx)
	require.True(t, found)
	require.Equal(t, uint64(7), signingEpoch)
}

func TestSetEffectiveEpochIndex_RejectsNonSDKContext(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)

	err := k.SetEffectiveEpochIndex(context.Background(), 5)
	require.Error(t, err)
	require.Contains(t, err.Error(), "requires sdk.Context")

	_, found := k.GetEffectiveEpochIndex(ctx)
	require.False(t, found)

	_, found = k.BlsKeeper.GetCurrentSigningEpochID(ctx)
	require.False(t, found)
}
