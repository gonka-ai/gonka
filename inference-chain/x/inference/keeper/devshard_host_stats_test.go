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

type capturePassLogger struct {
	infos []string
	warns []string
}

func (c *capturePassLogger) LogInfo(msg string, _ types.SubSystem, _ ...interface{}) {
	c.infos = append(c.infos, msg)
}
func (c *capturePassLogger) LogError(string, types.SubSystem, ...interface{}) {}
func (c *capturePassLogger) LogWarn(msg string, _ types.SubSystem, _ ...interface{}) {
	c.warns = append(c.warns, msg)
}
func (c *capturePassLogger) LogDebug(string, types.SubSystem, ...interface{}) {}

func sampledPolicy(rate uint32, applySampled bool, log *capturePassLogger) keeper.DevshardPassPolicy {
	p := keeper.DevshardPassPolicyFor(types.DevshardPassCount_DEVSHARD_PASS_COUNT_SAMPLED, rate)
	p.ApplySampled = applySampled
	if log != nil {
		p.Logger = log
	}
	return p
}

func unspecifiedPolicy(rate uint32, applySampled bool, log *capturePassLogger) keeper.DevshardPassPolicy {
	p := keeper.DevshardPassPolicyFor(types.DevshardPassCount_DEVSHARD_PASS_COUNT_UNSPECIFIED, rate)
	p.ApplySampled = applySampled
	if log != nil {
		p.Logger = log
	}
	return p
}

func derivedPolicy(rate uint32, applySampled bool, log *capturePassLogger) keeper.DevshardPassPolicy {
	p := keeper.DevshardPassPolicyFor(types.DevshardPassCount_DEVSHARD_PASS_COUNT_DERIVED, rate)
	p.ApplySampled = applySampled
	if log != nil {
		p.Logger = log
	}
	return p
}

func TestAggregateDevshardHostStats_OldDerivedPath(t *testing.T) {
	const assigned, slots = 8000, 16
	legacy := types.DevshardSettlementHostStats{SlotId: 0, Missed: 0, Invalid: 3}
	reported := types.DevshardSettlementHostStats{SlotId: 0, Missed: 0, Invalid: 10, Validated: 90, Finished: 100}

	for _, tc := range []struct {
		name   string
		policy keeper.DevshardPassPolicy
	}{
		{"DERIVED", derivedPolicy(1000, false, &capturePassLogger{})},
		{"DERIVED with apply_sampled on", derivedPolicy(1000, true, &capturePassLogger{})},
		{"UNSPECIFIED with apply_sampled off", unspecifiedPolicy(1000, false, &capturePassLogger{})},
	} {
		t.Run(tc.name+" legacy payload", func(t *testing.T) {
			p := &types.Participant{Address: "gonka1x"}
			require.NoError(t, keeper.AggregateDevshardHostStatsIntoCurrentEpochStats(p, legacy, assigned, slots, tc.policy))
			require.Equal(t, uint64(7997), p.CurrentEpochStats.ValidatedInferences, "assigned-missed-invalid; flag ignored")
			require.Equal(t, uint64(3), p.CurrentEpochStats.InvalidatedInferences)
			require.Equal(t, uint64(8000), p.CurrentEpochStats.InferenceCount)
			log := tc.policy.Logger.(*capturePassLogger)
			require.Empty(t, log.infos)
			require.Empty(t, log.warns, "old derived path never logs sampled-vs-derived")
		})
		t.Run(tc.name+" reported counts", func(t *testing.T) {
			p := &types.Participant{Address: "gonka1x"}
			require.NoError(t, keeper.AggregateDevshardHostStatsIntoCurrentEpochStats(p, reported, assigned, slots, tc.policy))
			require.Equal(t, uint64(7990), p.CurrentEpochStats.ValidatedInferences, "derived, not capped HostStats.validated")
			log := tc.policy.Logger.(*capturePassLogger)
			require.Empty(t, log.infos)
			require.Empty(t, log.warns)
		})
	}

	tooMany := types.DevshardSettlementHostStats{SlotId: 0, Validated: 8000*16 + 1}
	p := &types.Participant{Address: "gonka1x"}
	require.NoError(t, keeper.AggregateDevshardHostStatsIntoCurrentEpochStats(p, tooMany, assigned, slots, derivedPolicy(1000, false, nil)))
	require.Equal(t, uint64(8000), p.CurrentEpochStats.ValidatedInferences, "0.2.16 does not extra-check validated")
}

func TestAggregateDevshardHostStats_SampledLogOnly(t *testing.T) {
	const assigned, slots = 8000, 16
	stats := types.DevshardSettlementHostStats{SlotId: 0, Missed: 0, Invalid: 10, Validated: 90, Finished: 100}

	log := &capturePassLogger{}
	p := &types.Participant{Address: "gonka1x"}
	require.NoError(t, keeper.AggregateDevshardHostStatsIntoCurrentEpochStats(p, stats, assigned, slots, sampledPolicy(1000, false, log)))
	require.Equal(t, uint64(7990), p.CurrentEpochStats.ValidatedInferences, "flag off keeps derived punishment")
	require.NotEqual(t, uint64(22), p.CurrentEpochStats.ValidatedInferences)
	require.Equal(t, []string{"devshard derived pass count exceeds SPRT cap"}, log.warns)
	require.Empty(t, log.infos)

	withinCap := types.DevshardSettlementHostStats{SlotId: 0, Missed: 0, Invalid: 1, Validated: 2, Finished: 5}
	log = &capturePassLogger{}
	p = &types.Participant{Address: "gonka1x"}
	require.NoError(t, keeper.AggregateDevshardHostStatsIntoCurrentEpochStats(p, withinCap, 5, slots, sampledPolicy(10000, false, log)))
	require.Equal(t, uint64(4), p.CurrentEpochStats.ValidatedInferences, "derived 4, not sampled 2")
	require.Equal(t, []string{"devshard sampled pass count"}, log.infos)
	require.Empty(t, log.warns, "derived 4 is within cap 12")

	legacy := types.DevshardSettlementHostStats{SlotId: 0, Missed: 0, Invalid: 3}
	p = &types.Participant{Address: "gonka1x"}
	require.NoError(t, keeper.AggregateDevshardHostStatsIntoCurrentEpochStats(p, legacy, assigned, slots, sampledPolicy(1000, false, nil)))
	require.Equal(t, uint64(7997), p.CurrentEpochStats.ValidatedInferences, "flag off does not need validated/finished fields")

	tooMany := types.DevshardSettlementHostStats{SlotId: 0, Validated: 8000*16 + 1}
	err := keeper.AggregateDevshardHostStatsIntoCurrentEpochStats(&types.Participant{Address: "gonka1x"}, tooMany, assigned, slots, sampledPolicy(1000, false, nil))
	require.ErrorContains(t, err, "validated count")
}

func TestAggregateDevshardHostStats_SampledApply(t *testing.T) {
	const assigned, slots = 8000, 16
	stats := types.DevshardSettlementHostStats{SlotId: 0, Missed: 0, Invalid: 10, Validated: 90, Finished: 100}
	legacy := types.DevshardSettlementHostStats{SlotId: 0, Missed: 0, Invalid: 3}

	log := &capturePassLogger{}
	p := &types.Participant{Address: "gonka1x"}
	require.NoError(t, keeper.AggregateDevshardHostStatsIntoCurrentEpochStats(p, stats, assigned, slots, sampledPolicy(1000, true, log)))
	require.Equal(t, uint64(22), p.CurrentEpochStats.ValidatedInferences, "flag on applies sampled, not derived 7990")
	require.Equal(t, []string{"devshard derived pass count exceeds SPRT cap"}, log.warns)

	p = &types.Participant{Address: "gonka1x"}
	require.NoError(t, keeper.AggregateDevshardHostStatsIntoCurrentEpochStats(p, legacy, assigned, slots, sampledPolicy(1000, true, nil)))
	require.Equal(t, uint64(0), p.CurrentEpochStats.ValidatedInferences, "sampled with omitted validated credits 0")

	p = &types.Participant{Address: "gonka1x"}
	require.NoError(t, keeper.AggregateDevshardHostStatsIntoCurrentEpochStats(p, stats, assigned, slots, sampledPolicy(10000, true, nil)))
	require.Equal(t, uint64(90), p.CurrentEpochStats.ValidatedInferences, "within the cap the reported count is used")

	none := types.DevshardSettlementHostStats{SlotId: 0, Missed: 0, Invalid: 3, Finished: 50}
	p = &types.Participant{Address: "gonka1x"}
	require.NoError(t, keeper.AggregateDevshardHostStatsIntoCurrentEpochStats(p, none, assigned, slots, sampledPolicy(1000, true, nil)))
	require.Equal(t, uint64(0), p.CurrentEpochStats.ValidatedInferences, "a sampled version reporting no passes gets none")

	partial := types.DevshardSettlementHostStats{SlotId: 0, Missed: 0, Invalid: 0, Validated: 4}
	p = &types.Participant{Address: "gonka1x"}
	require.NoError(t, keeper.AggregateDevshardHostStatsIntoCurrentEpochStats(p, partial, assigned, slots, sampledPolicy(1000, true, nil)))
	require.Equal(t, uint64(2), p.CurrentEpochStats.ValidatedInferences, "SAMPLED does not extra-verify finished")

	inflated := types.DevshardSettlementHostStats{SlotId: 0, Missed: 0, Invalid: 0, Validated: 99999, Finished: 99999}
	p = &types.Participant{Address: "gonka1x"}
	require.NoError(t, keeper.AggregateDevshardHostStatsIntoCurrentEpochStats(p, inflated, assigned, slots, sampledPolicy(1000, true, nil)))
	require.Equal(t, uint64(1602), p.CurrentEpochStats.ValidatedInferences, "finished above completed is clamped in the cap, not rejected")

	tooMany := types.DevshardSettlementHostStats{SlotId: 0, Validated: 8000*16 + 1}
	err := keeper.AggregateDevshardHostStatsIntoCurrentEpochStats(&types.Participant{Address: "gonka1x"}, tooMany, assigned, slots, sampledPolicy(1000, true, nil))
	require.ErrorContains(t, err, "validated count", "flag on still extra-checks sampled-era values")
}
