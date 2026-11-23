package escrow

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const surplusHoldTicks = 40

// Test flow:
//  1. Decide one scheduled tick of a model short of its guarantee with money short, so a capacity shortfall is wanted.
//  2. Decide forty scheduled ticks of the same model with its guarantee met, each preceded by a wakeup tick, then one more scheduled tick.
//  3. Assert the shortfall tick itself is not held, every wakeup and scheduled tick of the forty is, wakeups neither shorten nor extend the hold, and the tick after them is free.
func TestASurplusRetireIsHeldForOneDemandWindowAfterACapacityShortfall(t *testing.T) {
	planner := fundingPlanner{models: map[string]*modelFunding{}, escrows: map[string]*escrowFunding{}}
	now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	shortReading := guaranteeReading(false, false)
	shortReading.state.MoneyShort = true
	metReading := guaranteeReading(true, false)

	short, _, _ := planner.decide("qwen", shortReading, true, now)
	require.False(t, short.SurplusRetiresHeld, "the shortfall tick")
	for tick := range surplusHoldTicks {
		now = now.Add(TickInterval)
		wakeup, _, _ := planner.decide("qwen", metReading, false, now)
		require.True(t, wakeup.SurplusRetiresHeld, "wakeup before scheduled tick %d", tick)
		held, _, _ := planner.decide("qwen", metReading, true, now)
		require.True(t, held.SurplusRetiresHeld, "scheduled tick %d", tick)
	}
	now = now.Add(TickInterval)
	free, _, _ := planner.decide("qwen", metReading, true, now)
	require.False(t, free.SurplusRetiresHeld, "the tick after the window")
}

// Test flow:
//  1. Decide one wakeup tick of a model short of its guarantee with money short, then one scheduled tick of the same model with its guarantee met.
//  2. Assert the wakeup tick armed the hold, so the scheduled tick that follows is held.
func TestAWakeupTickWithACapacityShortfallArmsTheSurplusHold(t *testing.T) {
	planner := fundingPlanner{models: map[string]*modelFunding{}, escrows: map[string]*escrowFunding{}}
	now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	shortReading := guaranteeReading(false, false)
	shortReading.state.MoneyShort = true

	planner.decide("qwen", shortReading, false, now)
	held, _, _ := planner.decide("qwen", guaranteeReading(true, false), true, now.Add(TickInterval))

	require.True(t, held.SurplusRetiresHeld, "the scheduled tick after the wakeup shortfall")
}

// Test flow:
//  1. Decide one scheduled tick of a model with its guarantee met and money short, so only a capacity shortfall is wanted, and one scheduled tick of a model short of its spread with money not short, so no capacity shortfall is wanted.
//  2. Decide a scheduled tick of each model with its guarantee met.
//  3. Assert the tick after the capacity shortfall is held and the tick after the shortfall without a capacity part is not.
func TestOnlyACapacityShortfallArmsTheSurplusHold(t *testing.T) {
	planner := fundingPlanner{models: map[string]*modelFunding{}, escrows: map[string]*escrowFunding{}}
	now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	capacityReading := guaranteeReading(true, false)
	capacityReading.state.MoneyShort = true
	spreadReading := guaranteeReading(true, false)
	spreadReading.state.TargetCount = 2

	_, capacityDecision, _ := planner.decide("capacity", capacityReading, true, now)
	_, spreadDecision, _ := planner.decide("spread", spreadReading, true, now)
	require.Positive(t, capacityDecision.Shortfalls.Capacity, "the capacity reading wants a capacity shortfall")
	require.Positive(t, spreadDecision.Shortfalls.Spread, "the spread reading wants a spread shortfall")
	require.Zero(t, spreadDecision.Shortfalls.Capacity, "the spread reading wants no capacity shortfall")
	now = now.Add(TickInterval)
	afterCapacity, _, _ := planner.decide("capacity", guaranteeReading(true, false), true, now)
	afterSpread, _, _ := planner.decide("spread", guaranteeReading(true, false), true, now)

	require.True(t, afterCapacity.SurplusRetiresHeld, "the tick after a capacity shortfall")
	require.False(t, afterSpread.SurplusRetiresHeld, "the tick after a shortfall without a capacity part")
}
