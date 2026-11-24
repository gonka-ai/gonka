package funding

import (
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
)

const (
	perEscrowWantedCount = 3
	perEscrowPeak        = 3_000_000
)

func perEscrowModel(regulars int) ModelState {
	escrows := make([]EscrowState, 0, regulars)
	for index := range regulars {
		starved := starvedEscrow("regular-"+strconv.Itoa(index), 0)
		starved.Money.Returning = 2 * perEscrowPeak
		escrows = append(escrows, starved)
	}
	state := healthyModel()
	state.Escrows, state.Counted = escrows, regulars
	state.TargetCount, state.TempCount, state.ReserveCount, state.FullContextSlots = 1, 0, 0, 0
	state.DemandWindowFull, state.DemandPeak = true, perEscrowPeak
	need := Plan(state).Need
	state.Amount = (need-1)/perEscrowWantedCount + state.CreateFee + state.FullCost
	return state
}

// Test flow:
//  1. Build a model of regulars whose money covers the need, with the demand peak and the amount set so the need is one unit over three escrows' worth of the create-sized capacity, which is the create fee short of the old surplus unit.
//  2. Plan it with three regulars, four and five.
//  3. Assert the need wants four escrows at the capacity unit, and the surplus is held only from the fifth regular, the same count the capacity rule would stop creating at.
func TestTheSurplusIsHeldOnlyAboveTheEscrowCountTheCapacityRuleSizesTheNeedAt(t *testing.T) {
	t.Parallel()
	state := perEscrowModel(perEscrowWantedCount + 1)
	require.Equal(t, perEscrowWantedCount+1, Plan(state).NeedEscrows, "escrows the need is sized at")
	require.False(t, Plan(perEscrowModel(perEscrowWantedCount)).SurplusHeld, "three regulars")
	require.False(t, Plan(state).SurplusHeld, "four regulars")
	require.True(t, Plan(perEscrowModel(perEscrowWantedCount+2)).SurplusHeld, "five regulars")
}
