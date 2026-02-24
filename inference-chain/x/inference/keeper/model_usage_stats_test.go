package keeper_test

import (
	"testing"

	keepertest "github.com/productscience/inference/testutil/keeper"
	keeper2 "github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func TestModelUsage_AddModelUsageSample_AggregatesAndCutover(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)

	require.NoError(t, k.AddModelUsageSample(ctx, "model-a", 1000, 10, 20))
	require.NoError(t, k.AddModelUsageSample(ctx, "model-a", 1500, 5, 30))
	require.NoError(t, k.AddModelUsageSample(ctx, "model-b", 1100, 7, -10))

	cutover, ok := k.GetModelUsageCutoverTimestamp(ctx)
	require.True(t, ok)
	require.Equal(t, int64(1000), cutover)

	stats := k.GetModelUsageSummaryByTime(ctx, 1000, 1999)
	require.Len(t, stats, 2)
	requireModelSummary(t, stats, "model-a", 2, 15, 50)
	requireModelSummary(t, stats, "model-b", 1, 7, 0)

	require.NoError(t, k.AddModelUsageSample(ctx, "model-a", 500, 2, 4))

	cutover2, ok := k.GetModelUsageCutoverTimestamp(ctx)
	require.True(t, ok)
	require.Equal(t, cutover, cutover2)
}

func TestModelUsage_GetSummaryByModelAndTime_CutoverBranches(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)

	legacyInference := types.Inference{
		InferenceId:          "legacy-1",
		RequestedBy:          "dev-1",
		Model:                "model-a",
		Status:               types.InferenceStatus_FINISHED,
		PromptTokenCount:     3,
		CompletionTokenCount: 2,
		EndBlockTimestamp:    1500,
		EpochId:              1,
		ActualCost:           70,
	}
	require.NoError(t, k.SetDeveloperStats(ctx, legacyInference))

	noCutoverStats := k.GetSummaryByModelAndTime(ctx, 1000, 1999)
	require.Len(t, noCutoverStats, 1)
	requireModelSummary(t, noCutoverStats, "model-a", 1, 5, 70)

	require.NoError(t, k.AddModelUsageSample(ctx, "model-a", 2000, 11, 40))

	beforeCutoverStats := k.GetSummaryByModelAndTime(ctx, 1000, 1999)
	require.Len(t, beforeCutoverStats, 1)
	requireModelSummary(t, beforeCutoverStats, "model-a", 1, 5, 70)

	afterCutoverStats := k.GetSummaryByModelAndTime(ctx, 2000, 2500)
	require.Len(t, afterCutoverStats, 1)
	requireModelSummary(t, afterCutoverStats, "model-a", 1, 11, 40)

	spanningStats := k.GetSummaryByModelAndTime(ctx, 1000, 2500)
	require.Len(t, spanningStats, 1)
	requireModelSummary(t, spanningStats, "model-a", 2, 16, 110)
}

func requireModelSummary(t *testing.T, stats map[string]keeper2.StatsSummary, model string, count int, tokens int64, actualCost int64) {
	t.Helper()

	summary, ok := stats[model]
	require.True(t, ok)
	require.Equal(t, count, summary.InferenceCount)
	require.Equal(t, tokens, summary.TokensUsed)
	require.Equal(t, actualCost, summary.ActualCost)
}
