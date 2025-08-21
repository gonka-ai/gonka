package keeper_test

import (
	"context"
	"strconv"
	"testing"

	testutil "github.com/productscience/inference/testutil"
	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/testutil/nullify"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

// Prevent strconv unused error
var _ = strconv.IntSize

func createNEpochPerformanceSummary(keeper keeper.Keeper, ctx context.Context, n int) []types.EpochPerformanceSummary {
	items := make([]types.EpochPerformanceSummary, n)
	for i := range items {
		items[i].EpochIndex = uint64(i)
		items[i].ParticipantId = testutil.Bech32Addr(i)

		keeper.SetEpochPerformanceSummary(ctx, items[i])
	}
	return items
}

func TestEpochPerformanceSummaryGet(t *testing.T) {
	keeper, ctx := keepertest.InferenceKeeper(t)
	items := createNEpochPerformanceSummary(keeper, ctx, 10)
	for _, item := range items {
		rst, found := keeper.GetEpochPerformanceSummary(ctx,
			item.EpochIndex,
			item.ParticipantId,
		)
		require.True(t, found)
		require.Equal(t,
			nullify.Fill(&item),
			nullify.Fill(&rst),
		)
	}
}
func TestEpochPerformanceSummaryRemove(t *testing.T) {
	keeper, ctx := keepertest.InferenceKeeper(t)
	items := createNEpochPerformanceSummary(keeper, ctx, 10)
	for _, item := range items {
		keeper.RemoveEpochPerformanceSummary(ctx,
			item.EpochIndex,
			item.ParticipantId,
		)
		_, found := keeper.GetEpochPerformanceSummary(ctx,
			item.EpochIndex,
			item.ParticipantId,
		)
		require.False(t, found)
	}
}

func TestEpochPerformanceSummaryGetAll(t *testing.T) {
	keeper, ctx := keepertest.InferenceKeeper(t)
	items := createNEpochPerformanceSummary(keeper, ctx, 10)
	require.ElementsMatch(t,
		nullify.Fill(items),
		nullify.Fill(keeper.GetAllEpochPerformanceSummary(ctx)),
	)
}

func TestEpochPerformanceSummaryGetByParticipants(t *testing.T) {
	keeper, ctx := keepertest.InferenceKeeper(t)
	items := createNEpochPerformanceSummary(keeper, ctx, 10)

	expectedItems := items[:1]
	require.ElementsMatch(t,
		nullify.Fill(expectedItems),
		nullify.Fill(keeper.GetParticipantsEpochSummaries(ctx, []string{testutil.Bech32Addr(0), testutil.Bech32Addr(1), testutil.Bech32Addr(2)}, 0)),
	)

	extraItem := types.EpochPerformanceSummary{}
	extraItem.EpochIndex = uint64(1)
	extraItem.ParticipantId = testutil.Bech32Addr(2)

	keeper.SetEpochPerformanceSummary(ctx, extraItem)
	expectedItems = append(items[1:2], extraItem)
	require.ElementsMatch(t,
		nullify.Fill(expectedItems),
		nullify.Fill(keeper.GetParticipantsEpochSummaries(ctx, []string{testutil.Bech32Addr(0), testutil.Bech32Addr(1), testutil.Bech32Addr(2)}, 1)),
	)
}
