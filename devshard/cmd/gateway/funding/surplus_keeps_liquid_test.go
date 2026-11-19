package funding

import (
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
)

const (
	surplusLiquidTicks           = 40
	surplusLiquidRegulars        = 15
	surplusLiquidTarget          = 14
	surplusLiquidMaxUnsettled    = 8192
	surplusLiquidBucketTokens    = 100
	surplusLiquidCurrentLabel    = 7
	surplusLiquidRegularFree     = 600_000
	surplusLiquidFreeStep        = 1
	surplusLiquidStarvedFree     = 10_000
	surplusLiquidStarvedReturns  = 500_000
	surplusLiquidVictimNeedPeak  = 5_300_000
	surplusLiquidVictimSparePeak = 4_000_000
)

func surplusLiquidRegularEscrows(count int) []EscrowState {
	regulars := make([]EscrowState, 0, count)
	for index := range count {
		regulars = append(regulars, fullEscrow("regular-"+strconv.Itoa(index), surplusLiquidRegularFree+uint64(index)*surplusLiquidFreeStep))
	}
	return regulars
}

func surplusLiquidModel(escrows []EscrowState, demandPeak uint64) ModelState {
	state := healthyModel()
	state.Escrows = escrows
	state.Counted = len(escrows)
	state.TargetCount, state.ReserveCount, state.TempCount = surplusLiquidTarget, 0, 0
	state.MaxUnsettled, state.BucketTokens = surplusLiquidMaxUnsettled, surplusLiquidBucketTokens
	state.CurrentLabel, state.DemandWindowFull, state.DemandPeak = surplusLiquidCurrentLabel, true, demandPeak
	return state
}

func surplusLiquidStarvedEscrow() EscrowState {
	starved := starvedEscrow("starved", surplusLiquidStarvedFree)
	starved.Money.Returning = surplusLiquidStarvedReturns
	return starved
}

// Test flow:
//  1. Build a model one regular above its target whose liquid money exceeds the need but would fall below it without the retire victim: once with the victim a full escrow, once with the victim a starved escrow holding returning money.
//  2. Plan forty scheduled ticks at that constant demand, applying each decision to the next tick's state as the manager would.
//  3. Assert no tick creates or retires anything: the surplus retire is withheld, so the capacity rule has nothing to create back.
func TestASurplusRetireIsWithheldWhileTheLiquidMoneyWithoutItsVictimIsBelowTheNeed(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name    string
		escrows []EscrowState
	}{
		{name: "a full victim", escrows: surplusLiquidRegularEscrows(surplusLiquidRegulars)},
		{name: "a starved victim holding returning money", escrows: append(surplusLiquidRegularEscrows(surplusLiquidTarget), surplusLiquidStarvedEscrow())},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			state := surplusLiquidModel(testCase.escrows, surplusLiquidVictimNeedPeak)
			for tick := range surplusLiquidTicks {
				decision := Plan(state)
				require.LessOrEqual(t, decision.Need, decision.Liquid, "tick %d need against liquid", tick)
				require.Empty(t, decision.Retires, "tick %d retires", tick)
				require.Empty(t, decision.Creates, "tick %d creates", tick)
				state = applyDecision(state, decision, tick)
			}
		})
	}
}

// Test flow:
//  1. Build the same full-escrow model with a demand low enough that the liquid money without the least-free escrow still covers the need.
//  2. Plan forty scheduled ticks, applying each decision to the next tick's state.
//  3. Assert the surplus retire of the least-free escrow fires exactly once, the model settles at its target, and nothing is ever created.
func TestASurplusRetireStillFiresWhenTheLiquidMoneyWithoutItsVictimCoversTheNeed(t *testing.T) {
	t.Parallel()
	escrows := surplusLiquidRegularEscrows(surplusLiquidRegulars)
	state := surplusLiquidModel(escrows, surplusLiquidVictimSparePeak)
	retires := make([]Retire, 0, surplusLiquidTicks)
	for tick := range surplusLiquidTicks {
		decision := Plan(state)
		require.Empty(t, decision.Creates, "tick %d creates", tick)
		retires = append(retires, decision.Retires...)
		state = applyDecision(state, decision, tick)
	}
	require.Equal(t, []Retire{{EscrowID: escrows[0].ID, Reason: ReasonSurplus}}, retires)
	require.Len(t, state.Escrows, surplusLiquidTarget)
}
