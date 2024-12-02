package keeper_test

import (
	"context"
	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	"strconv"
	"testing"

	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/testutil/nullify"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

// Prevent strconv unused error
var _ = strconv.IntSize

func createNEpochGroupData(keeper keeper.Keeper, ctx context.Context, n int) []types.EpochGroupData {
	items := make([]types.EpochGroupData, n)
	for i := range items {
		items[i].PocStartBlockHeight = uint64(i)
		items[i].MemberSeedSignatures = []*types.SeedSignature{}
		keeper.SetEpochGroupData(ctx, items[i])
	}
	return items
}

func TestRawRoundTrip(t *testing.T) {
	registry := codectypes.NewInterfaceRegistry()
	cdc := codec.NewProtoCodec(registry)
	epochGroupData := types.EpochGroupData{
		PocStartBlockHeight: 1,
	}
	bytes := cdc.MustMarshal(&epochGroupData)
	roundTripped := types.EpochGroupData{}
	cdc.Unmarshal(bytes, &roundTripped)
	require.Equal(t, epochGroupData, roundTripped)
}

func TestEpochGroupDataGet(t *testing.T) {
	keeper, ctx := keepertest.InferenceKeeper(t)
	items := createNEpochGroupData(keeper, ctx, 10)
	for _, item := range items {
		rst, found := keeper.GetEpochGroupData(ctx,
			item.PocStartBlockHeight,
		)
		require.True(t, found)
		// This will be nil if MemberSeedSignature is empty!!
		require.Nil(t, rst.MemberSeedSignatures)
		require.Equal(t,
			nullify.Fill(&item),
			nullify.Fill(&rst),
		)
	}
}
func TestEpochGroupDataRemove(t *testing.T) {
	keeper, ctx := keepertest.InferenceKeeper(t)
	items := createNEpochGroupData(keeper, ctx, 10)
	for _, item := range items {
		keeper.RemoveEpochGroupData(ctx,
			item.PocStartBlockHeight,
		)
		_, found := keeper.GetEpochGroupData(ctx,
			item.PocStartBlockHeight,
		)
		require.False(t, found)
	}
}

func TestEpochGroupDataGetAll(t *testing.T) {
	keeper, ctx := keepertest.InferenceKeeper(t)
	items := createNEpochGroupData(keeper, ctx, 10)
	require.ElementsMatch(t,
		nullify.Fill(items),
		nullify.Fill(keeper.GetAllEpochGroupData(ctx)),
	)
}
