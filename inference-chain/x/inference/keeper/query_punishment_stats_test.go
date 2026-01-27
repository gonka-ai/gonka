package keeper_test

import (
	"testing"

	"github.com/productscience/inference/testutil"
	keeper2 "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

func TestGetPunishmentStats_NotPunished(t *testing.T) {
	k, ctx, _ := keeper2.InferenceKeeperReturningMocks(t)

	// Create a participant with good performance (low miss rate)
	epochIndex := uint64(1)
	participantId := testutil.Executor

	summary := types.EpochPerformanceSummary{
		EpochIndex:      epochIndex,
		ParticipantId:   participantId,
		InferenceCount:  100, // 100 completed
		MissedRequests:  5,   // Only 5 missed (5% rate - should pass)
		EarnedCoins:     1000,
		RewardedCoins:   1000, // Got full reward
	}
	err := k.SetEpochPerformanceSummary(ctx, summary)
	require.NoError(t, err)

	// Query punishment stats
	stats, err := k.GetPunishmentStats(ctx, epochIndex, participantId)
	require.NoError(t, err)
	require.NotNil(t, stats)

	// Should NOT be punished (5% miss rate is acceptable)
	require.False(t, stats.WasPunished, "participant with 5%% miss rate should not be punished")
	require.Equal(t, uint64(1000), stats.RewardedCoins) // Full reward
	require.Equal(t, uint64(5), stats.MissedRequests)
	require.Equal(t, uint64(105), stats.TotalRequests)
}

func TestGetPunishmentStats_Punished(t *testing.T) {
	k, ctx, _ := keeper2.InferenceKeeperReturningMocks(t)

	// Create a participant with bad performance (high miss rate)
	epochIndex := uint64(1)
	participantId := testutil.Executor

	summary := types.EpochPerformanceSummary{
		EpochIndex:      epochIndex,
		ParticipantId:   participantId,
		InferenceCount:  50,  // 50 completed
		MissedRequests:  50,  // 50 missed (50% rate - should fail)
		EarnedCoins:     1000,
		RewardedCoins:   0,   // Got zero reward (punished)
	}
	err := k.SetEpochPerformanceSummary(ctx, summary)
	require.NoError(t, err)

	// Query punishment stats
	stats, err := k.GetPunishmentStats(ctx, epochIndex, participantId)
	require.NoError(t, err)
	require.NotNil(t, stats)

	// Should BE punished (50% miss rate is unacceptable)
	require.True(t, stats.WasPunished, "participant with 50%% miss rate should be punished")
	require.Equal(t, uint64(0), stats.RewardedCoins) // Zero reward (punished)
	require.Equal(t, uint64(50), stats.MissedRequests)
	require.Equal(t, uint64(100), stats.TotalRequests)
}

func TestGetPunishmentStats_NoWorkAssigned(t *testing.T) {
	k, ctx, _ := keeper2.InferenceKeeperReturningMocks(t)

	// Create a participant with no work assigned
	epochIndex := uint64(1)
	participantId := testutil.Executor

	summary := types.EpochPerformanceSummary{
		EpochIndex:      epochIndex,
		ParticipantId:   participantId,
		InferenceCount:  0,    // No completed work
		MissedRequests:  0,    // No missed requests
		EarnedCoins:     0,
		RewardedCoins:   1000, // Still got epoch reward (PoC weight based)
	}
	err := k.SetEpochPerformanceSummary(ctx, summary)
	require.NoError(t, err)

	// Query punishment stats
	stats, err := k.GetPunishmentStats(ctx, epochIndex, participantId)
	require.NoError(t, err)
	require.NotNil(t, stats)

	// Should NOT be punished (no work = no punishment, per accountsettle.go:53-54)
	require.False(t, stats.WasPunished, "participant with no work should not be punished")
	require.Equal(t, uint64(1000), stats.RewardedCoins) // Still got PoC reward
	require.Equal(t, uint64(0), stats.TotalRequests)
}

func TestGetPunishmentStats_MissedEverything(t *testing.T) {
	k, ctx, _ := keeper2.InferenceKeeperReturningMocks(t)

	// Create a participant who missed ALL assigned work
	epochIndex := uint64(1)
	participantId := testutil.Executor

	summary := types.EpochPerformanceSummary{
		EpochIndex:      epochIndex,
		ParticipantId:   participantId,
		InferenceCount:  0,   // Completed nothing
		MissedRequests:  100, // Missed everything
		EarnedCoins:     0,
		RewardedCoins:   0,   // Got nothing
	}
	err := k.SetEpochPerformanceSummary(ctx, summary)
	require.NoError(t, err)

	// Query punishment stats
	stats, err := k.GetPunishmentStats(ctx, epochIndex, participantId)
	require.NoError(t, err)
	require.NotNil(t, stats)

	// Should BE punished (100% miss rate)
	require.True(t, stats.WasPunished, "participant with 100%% miss rate should be punished")
	require.Equal(t, uint64(100), stats.TotalRequests)
	require.True(t, decimal.NewFromInt(1).Equal(stats.MissedRatio))
}

func TestGetPunishmentStats_NotFound(t *testing.T) {
	k, ctx, _ := keeper2.InferenceKeeperReturningMocks(t)

	// Query for non-existent participant (use Executor2 which wasn't added)
	stats, err := k.GetPunishmentStats(ctx, 999, testutil.Executor2)

	require.Error(t, err)
	require.Nil(t, stats)
	require.Contains(t, err.Error(), "not found")
}

func TestGetPunishmentStats_AtThreshold(t *testing.T) {
	k, ctx, _ := keeper2.InferenceKeeperReturningMocks(t)

	// Create a participant right at the boundary
	// At n=100 with p0=0.10, critical value is ~14 misses (14%)
	epochIndex := uint64(1)
	participantId := testutil.Executor

	summary := types.EpochPerformanceSummary{
		EpochIndex:      epochIndex,
		ParticipantId:   participantId,
		InferenceCount:  90,  // 90 completed
		MissedRequests:  10,  // 10 missed (10% exactly)
		EarnedCoins:     1000,
		RewardedCoins:   1000, // Should still get reward at 10%
	}
	err := k.SetEpochPerformanceSummary(ctx, summary)
	require.NoError(t, err)

	// Query punishment stats
	stats, err := k.GetPunishmentStats(ctx, epochIndex, participantId)
	require.NoError(t, err)
	require.NotNil(t, stats)

	// At exactly 10%, the statistical test should PASS (not punished)
	// Because it's a one-sided test: we need to be SIGNIFICANTLY above p0
	require.False(t, stats.WasPunished, "participant at exactly 10%% should not be punished (threshold is ~14%% at n=100)")
}
