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

func settledSummary() types.EpochPerformanceSummary {
	return types.EpochPerformanceSummary{
		EpochIndex:          415,
		ParticipantId:       testutil.Executor,
		InferenceCount:      1200,
		MissedRequests:      3,
		EarnedCoins:         1_500_000_000,
		RewardedCoins:       9_000_000_000,
		ValidatedInferences: 40,
	}
}

func TestEpochPerformanceSummary_ValueOmitsKeyFields(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	want := settledSummary()

	gctx := ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())
	require.NoError(t, k.SetEpochPerformanceSummary(gctx, want))
	t.Logf("SetEpochPerformanceSummary gas: %d", gctx.GasMeter().GasConsumed())

	key := collections.Join(sdk.MustAccAddressFromBech32(testutil.Executor), uint64(415))
	raw, err := k.EpochPerformanceSummaries.Get(ctx, key)
	require.NoError(t, err)
	require.Empty(t, raw.ParticipantId)
	require.Zero(t, raw.EpochIndex)
	require.Equal(t, want.InferenceCount, raw.InferenceCount)

	got, found := k.GetEpochPerformanceSummary(ctx, 415, testutil.Executor)
	require.True(t, found)
	require.Equal(t, want, got)
	require.Equal(t, []types.EpochPerformanceSummary{want}, k.GetAllEpochPerformanceSummary(ctx))
	require.Equal(t, []types.EpochPerformanceSummary{want}, k.GetEpochPerformanceSummariesByParticipant(ctx, testutil.Executor))
	require.Equal(t, []types.EpochPerformanceSummary{want}, k.GetParticipantsEpochSummaries(ctx, []string{testutil.Executor}, 415))

	all, err := k.EpochPerformanceSummaryAll(ctx, &types.QueryAllEpochPerformanceSummaryRequest{})
	require.NoError(t, err)
	require.Equal(t, []types.EpochPerformanceSummary{want}, all.EpochPerformanceSummary)
	byEpoch, err := k.EpochPerformanceSummary(ctx, &types.QueryEpochPerformanceSummaryByEpochRequest{EpochIndex: 415})
	require.NoError(t, err)
	require.Equal(t, []types.EpochPerformanceSummary{want}, byEpoch.EpochPerformanceSummary)

	// The claim path reads the trimmed record, flips Claimed and writes it back under the same key.
	gctx = ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())
	got, found = k.GetEpochPerformanceSummary(gctx, 415, testutil.Executor)
	require.True(t, found)
	got.Claimed = true
	require.NoError(t, k.SetEpochPerformanceSummary(gctx, got))
	t.Logf("claim mark (get + set) gas: %d", gctx.GasMeter().GasConsumed())
	want.Claimed = true
	got, _ = k.GetEpochPerformanceSummary(ctx, 415, testutil.Executor)
	require.Equal(t, want, got)
	require.Len(t, k.GetAllEpochPerformanceSummary(ctx), 1)
}

func TestEpochPerformanceSummary_KeepsFieldsWhenKeyCannotRestoreThem(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	upper := settledSummary()
	upper.ParticipantId = strings.ToUpper(testutil.Executor)
	require.NoError(t, k.SetEpochPerformanceSummary(ctx, upper))
	got, found := k.GetEpochPerformanceSummary(ctx, 415, testutil.Executor)
	require.True(t, found)
	require.Equal(t, upper, got)

	// An all-zero summary would encode to an empty value; it is stored whole.
	zero := types.EpochPerformanceSummary{EpochIndex: 416, ParticipantId: testutil.Executor}
	require.NoError(t, k.SetEpochPerformanceSummary(ctx, zero))
	raw, err := k.EpochPerformanceSummaries.Get(ctx, collections.Join(sdk.MustAccAddressFromBech32(testutil.Executor), uint64(416)))
	require.NoError(t, err)
	require.Equal(t, zero, raw)
}

func TestEpochPerformanceSummary_LegacyFullRecord(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	full := settledSummary()
	key := collections.Join(sdk.MustAccAddressFromBech32(testutil.Executor), uint64(415))
	require.NoError(t, k.EpochPerformanceSummaries.Set(ctx, key, full))
	got, found := k.GetEpochPerformanceSummary(ctx, 415, testutil.Executor)
	require.True(t, found)
	require.Equal(t, full, got)
	require.Equal(t, []types.EpochPerformanceSummary{full}, k.GetAllEpochPerformanceSummary(ctx))
}
