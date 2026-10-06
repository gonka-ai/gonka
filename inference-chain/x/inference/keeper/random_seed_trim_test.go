package keeper_test

import (
	"strings"
	"testing"

	"cosmossdk.io/collections"
	storetypes "cosmossdk.io/store/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/testutil"
	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/types"
)

func submittedSeed() types.RandomSeed {
	return types.RandomSeed{
		Participant: testutil.Executor,
		EpochIndex:  415,
		Signature:   strings.Repeat("ab", 64),
	}
}

func TestRandomSeed_ValueOmitsKeyFields(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	want := submittedSeed()

	gctx := ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())
	require.NoError(t, k.SetRandomSeed(gctx, want))
	t.Logf("SetRandomSeed gas: %d", gctx.GasMeter().GasConsumed())

	key := collections.Join(uint64(415), sdk.MustAccAddressFromBech32(testutil.Executor))
	raw, err := k.RandomSeeds.Get(ctx, key)
	require.NoError(t, err)
	require.Empty(t, raw.Participant)
	require.Zero(t, raw.EpochIndex)
	require.Equal(t, want.Signature, raw.Signature)

	gctx = ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())
	got, found := k.GetRandomSeed(gctx, 415, testutil.Executor)
	t.Logf("GetRandomSeed gas: %d", gctx.GasMeter().GasConsumed())
	require.True(t, found)
	require.Equal(t, want, got)

	list, err := k.ListRandomSeeds(ctx, &types.QueryRandomSeedsRequest{EpochIndex: 415})
	require.NoError(t, err)
	require.Equal(t, []*types.RandomSeed{&want}, list.Seeds)
}

func TestRandomSeed_KeepsFieldsWhenKeyCannotRestoreThem(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	upper := submittedSeed()
	upper.Participant = strings.ToUpper(testutil.Executor)
	require.NoError(t, k.SetRandomSeed(ctx, upper))
	got, found := k.GetRandomSeed(ctx, 415, testutil.Executor)
	require.True(t, found)
	require.Equal(t, upper, got)

	// An empty signature would leave an empty value; it is stored whole.
	empty := types.RandomSeed{Participant: testutil.Executor, EpochIndex: 416}
	require.NoError(t, k.SetRandomSeed(ctx, empty))
	raw, err := k.RandomSeeds.Get(ctx, collections.Join(uint64(416), sdk.MustAccAddressFromBech32(testutil.Executor)))
	require.NoError(t, err)
	require.Equal(t, empty, raw)
}

func TestRandomSeed_LegacyFullRecord(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	full := submittedSeed()
	key := collections.Join(uint64(415), sdk.MustAccAddressFromBech32(testutil.Executor))
	require.NoError(t, k.RandomSeeds.Set(ctx, key, full))
	got, found := k.GetRandomSeed(ctx, 415, testutil.Executor)
	require.True(t, found)
	require.Equal(t, full, got)
	list, err := k.ListRandomSeeds(ctx, &types.QueryRandomSeedsRequest{EpochIndex: 415})
	require.NoError(t, err)
	require.Equal(t, []*types.RandomSeed{&full}, list.Seeds)
}
