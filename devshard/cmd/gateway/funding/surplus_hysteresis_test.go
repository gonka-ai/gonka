package funding

import (
	"testing"

	"github.com/stretchr/testify/require"
)

const hysteresisTicks = 100

func hysteresisPeakWithNeed(state ModelState, need uint64) uint64 {
	slotsTerm := uint64(state.FullContextSlots) * state.Slot
	return (need - slotsTerm) * demandPeakPerHeadroom / (demandPeakPerHeadroom + 1)
}

// Test flow:
//  1. Build a model one regular above its target and measure the liquid money left without the surplus victim.
//  2. Alternate the demand peak for a hundred scheduled ticks between the need equal to that liquid money and one reserve above it, applying each decision to the next tick's state.
//  3. Assert no tick retires or creates anything: the retire margin of one escrow keeps the surplus rule off the capacity rule's boundary.
func TestLiquidMoneyOscillatingByOneReserveAroundTheNeedGivesNoRetireCreatePair(t *testing.T) {
	t.Parallel()
	escrows := surplusLiquidRegularEscrows(surplusLiquidRegulars)
	state := surplusLiquidModel(escrows, 0)
	withVictim := Plan(state).Liquid
	withoutVictim := withVictim - escrowLiquidMoney(state, escrows[0])
	lowPeak := hysteresisPeakWithNeed(state, withoutVictim)
	highPeak := lowPeak + state.FullCost
	require.LessOrEqual(t, Plan(withDemand(state, lowPeak)).Need, withoutVictim, "the low need leaves the old rule free to retire")
	for tick := range hysteresisTicks {
		state.DemandPeak = lowPeak
		if tick%2 == 1 {
			state.DemandPeak = highPeak
		}
		decision := Plan(state)
		require.Empty(t, decision.Retires, "tick %d retires", tick)
		require.Empty(t, decision.Creates, "tick %d creates", tick)
		state = applyDecision(state, decision, tick)
	}
	require.Len(t, state.Escrows, surplusLiquidRegulars)
}

func withDemand(state ModelState, peak uint64) ModelState {
	state.DemandPeak = peak
	return state
}

// Test flow:
//  1. Build the same model with a demand peak that leaves one whole escrow of liquid money above the need once the victim goes.
//  2. Plan forty scheduled ticks, applying each decision to the next tick's state.
//  3. Assert the surplus retire of the least-free escrow still fires exactly once and nothing is created.
func TestASurplusRetireFiresOnceTheLiquidMoneyWithoutItsVictimExceedsTheNeedByOneEscrow(t *testing.T) {
	t.Parallel()
	escrows := surplusLiquidRegularEscrows(surplusLiquidRegulars)
	state := surplusLiquidModel(escrows, 0)
	withoutVictim := Plan(state).Liquid - escrowLiquidMoney(state, escrows[0])
	state.DemandPeak = hysteresisPeakWithNeed(state, withoutVictim-capacityPerEscrow(state))
	retires := make([]Retire, 0, surplusLiquidTicks)
	for tick := range surplusLiquidTicks {
		decision := Plan(state)
		require.Empty(t, decision.Creates, "tick %d creates", tick)
		retires = append(retires, decision.Retires...)
		state = applyDecision(state, decision, tick)
	}
	require.Equal(t, []Retire{{EscrowID: escrows[0].ID, Reason: ReasonSurplus}}, retires)
}

// Test flow:
//  1. Build a model one regular above its target whose surplus would retire the least-free escrow on its third sample.
//  2. Plan it with the surplus hold on and with it off.
//  3. Assert the held decision retires nothing and the free one retires that escrow.
func TestTheSurplusHoldWithholdsOnlyTheSurplusRetire(t *testing.T) {
	t.Parallel()
	escrows := surplusLiquidRegularEscrows(surplusLiquidRegulars)
	state := surplusLiquidModel(escrows, surplusLiquidVictimSparePeak)
	state.SurplusStreak = surplusSamples - 1
	require.Equal(t, []Retire{{EscrowID: escrows[0].ID, Reason: ReasonSurplus}}, Plan(state).Retires, "retires with the hold off")
	state.SurplusRetiresHeld = true
	require.Empty(t, Plan(state).Retires, "retires with the hold on")
	state.Escrows = append(state.Escrows, nonceCapped(fullEscrow("capped", surplusLiquidRegularFree)))
	require.Equal(t, []Retire{{EscrowID: "capped", Reason: ReasonNonceCap}}, Plan(state).Retires, "retires of a nonce-capped escrow with the hold on")
}

// Test flow:
//  1. Build a healthy model with one idle starved current-label regular above its target, and another with budget pressure on a busy starved escrow.
//  2. Plan each with the surplus hold on.
//  3. Assert the idle-starved retire and the budget-pressure retire still fire: the hold withholds only the surplus retire.
func TestTheSurplusHoldLeavesIdleStarvedAndBudgetPressureRetiresAlone(t *testing.T) {
	t.Parallel()
	idle := modelWith(func(state *ModelState) {
		state.Escrows = append(state.Escrows, idleFor(starvedEscrow("3", 40_000), idleRetireAfter))
		state.Counted, state.SurplusRetiresHeld = 3, true
	})
	require.Equal(t, []Retire{{EscrowID: "3", Reason: ReasonIdleStarved}}, Plan(idle).Retires, "idle starved with the hold on")
	pressed := modelWith(func(state *ModelState) {
		state.Escrows = append(state.Escrows, busy(starvedEscrow("3", 50_000)), busy(starvedEscrow("4", 40_000)))
		state.Counted, state.MaxUnsettled, state.MoneyShort, state.SurplusRetiresHeld = 4, 4, true, true
	})
	require.Equal(t, []Retire{{EscrowID: "4", Reason: ReasonBudgetPressure}}, Plan(pressed).Retires, "budget pressure with the hold on")
}

// Test flow:
//  1. Arm a fresh hold and read it, then elapse forty ticks one at a time.
//  2. Assert it is inactive before arming, active through the thirty-ninth elapse and inactive after the fortieth, and that arming again restarts the window.
func TestASurplusHoldLastsOneDemandWindow(t *testing.T) {
	t.Parallel()
	var hold SurplusHold
	require.False(t, hold.Active(), "a fresh hold")
	hold.Arm()
	for tick := range demandSamples - 1 {
		hold.Elapse()
		require.True(t, hold.Active(), "after %d elapses", tick+1)
	}
	hold.Elapse()
	require.False(t, hold.Active(), "after a full window")
	hold.Arm()
	hold.Elapse()
	hold.Arm()
	for range demandSamples - 1 {
		hold.Elapse()
	}
	require.True(t, hold.Active(), "a re-armed hold restarts the window")
}

// Test flow:
//  1. Build a model at its target of current-label regulars with one idle starved regular among them, idle past the retire delay.
//  2. Plan it once at the target and once with one more current-label regular, and once with the starved escrow a temp.
//  3. Assert the idle starved regular is kept at the target, retired one above it, and a temp is retired at the target since the spread count never held it.
func TestAnIdleStarvedRetireNeverOpensASpreadShortfall(t *testing.T) {
	t.Parallel()
	starved := idleFor(starvedEscrow("starved", surplusLiquidStarvedFree), idleRetireAfter)
	atTarget := surplusLiquidModel(append(surplusLiquidRegularEscrows(surplusLiquidTarget-1), starved), surplusLiquidVictimSparePeak)
	require.Empty(t, Plan(atTarget).Retires, "at the target")
	aboveTarget := surplusLiquidModel(append(surplusLiquidRegularEscrows(surplusLiquidTarget), starved), surplusLiquidVictimSparePeak)
	require.Equal(t, []Retire{{EscrowID: "starved", Reason: ReasonIdleStarved}}, Plan(aboveTarget).Retires, "one above the target")
	starved.Temp = true
	asTemp := surplusLiquidModel(append(surplusLiquidRegularEscrows(surplusLiquidTarget), starved), surplusLiquidVictimSparePeak)
	require.Equal(t, []Retire{{EscrowID: "starved", Reason: ReasonIdleStarved}}, Plan(asTemp).Retires, "a temp at the target")
}
