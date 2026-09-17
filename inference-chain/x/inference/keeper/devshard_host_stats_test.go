package keeper_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
)

func TestDevshardPassPolicyFor(t *testing.T) {
	sampled := keeper.DevshardPassPolicyFor(types.DevshardPassCount_DEVSHARD_PASS_COUNT_SAMPLED, 1000)
	require.False(t, sampled.PassCount.Derived())
	require.Equal(t, uint32(1000), sampled.ValidationRateBps)

	derived := keeper.DevshardPassPolicyFor(types.DevshardPassCount_DEVSHARD_PASS_COUNT_DERIVED, 1000)
	require.True(t, derived.PassCount.Derived())
}

func TestDevshardSprtPassCap(t *testing.T) {
	require.Equal(t, uint64(2), keeper.DevshardSprtPassCap(0, 1000))
	require.Equal(t, uint64(2), keeper.DevshardSprtPassCap(1000, 0))
	require.Equal(t, uint64(202), keeper.DevshardSprtPassCap(1000, 1000))
	require.Equal(t, uint64(2002), keeper.DevshardSprtPassCap(1000, 10000))
}

func TestAggregateDevshardHostStats_PassPolicy(t *testing.T) {
	const assigned, slots = 8000, 16
	stats := types.DevshardSettlementHostStats{SlotId: 0, Missed: 0, Invalid: 10, Validated: 90, Finished: 100}

	p := &types.Participant{Address: "gonka1x"}
	require.NoError(t, keeper.AggregateDevshardHostStatsIntoCurrentEpochStats(p, stats, assigned, slots,
		keeper.DevshardPassPolicyFor(types.DevshardPassCount_DEVSHARD_PASS_COUNT_SAMPLED, 1000)))
	require.Equal(t, uint64(22), p.CurrentEpochStats.ValidatedInferences, "the cap follows finished work, not nonce padding")
	require.Equal(t, uint64(10), p.CurrentEpochStats.InvalidatedInferences)
	require.Equal(t, uint64(8000), p.CurrentEpochStats.InferenceCount)

	p = &types.Participant{Address: "gonka1x"}
	require.NoError(t, keeper.AggregateDevshardHostStatsIntoCurrentEpochStats(p, stats, assigned, slots,
		keeper.DevshardPassPolicyFor(types.DevshardPassCount_DEVSHARD_PASS_COUNT_SAMPLED, 10000)))
	require.Equal(t, uint64(90), p.CurrentEpochStats.ValidatedInferences, "within the cap the reported count is used")

	legacy := types.DevshardSettlementHostStats{SlotId: 0, Missed: 0, Invalid: 3}
	p = &types.Participant{Address: "gonka1x"}
	require.NoError(t, keeper.AggregateDevshardHostStatsIntoCurrentEpochStats(p, legacy, assigned, slots,
		keeper.DevshardPassPolicyFor(types.DevshardPassCount_DEVSHARD_PASS_COUNT_DERIVED, 1000)))
	require.Equal(t, uint64(7997), p.CurrentEpochStats.ValidatedInferences, "legacy versions keep completed - invalid")
	require.Equal(t, uint64(3), p.CurrentEpochStats.InvalidatedInferences)

	p = &types.Participant{Address: "gonka1x"}
	err := keeper.AggregateDevshardHostStatsIntoCurrentEpochStats(p, legacy, assigned, slots,
		keeper.DevshardPassPolicyFor(types.DevshardPassCount_DEVSHARD_PASS_COUNT_SAMPLED, 1000))
	require.ErrorContains(t, err, "invalid count", "a sampled version cannot report invalidations without finishes")

	reported := types.DevshardSettlementHostStats{SlotId: 0, Missed: 0, Invalid: 3, Finished: 50}
	p = &types.Participant{Address: "gonka1x"}
	require.NoError(t, keeper.AggregateDevshardHostStatsIntoCurrentEpochStats(p, reported, assigned, slots,
		keeper.DevshardPassPolicyFor(types.DevshardPassCount_DEVSHARD_PASS_COUNT_SAMPLED, 1000)))
	require.Equal(t, uint64(0), p.CurrentEpochStats.ValidatedInferences, "a sampled version reporting no passes gets none")
}
