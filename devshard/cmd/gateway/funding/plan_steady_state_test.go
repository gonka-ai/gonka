package funding

import (
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
)

const (
	steadyStateTicks        = 16
	steadyStateTarget       = 14
	steadyStateReserves     = 2
	steadyStateTemps        = 1
	steadyStateMaxUnsettled = 8192
	steadyStateCurrentLabel = 7
	steadyStateOlderLabel   = 6
	steadyStateRegularFree  = 900_000
	steadyStateFreeStep     = 1_000
	steadyStateSpareFree    = 1_000_000
)

func steadyStateModel(extra EscrowState) ModelState {
	state := healthyModel()
	state.Escrows = nil
	for index := range steadyStateTarget {
		regular := fullEscrow("regular-"+strconv.Itoa(index), steadyStateRegularFree+uint64(index)*steadyStateFreeStep)
		regular.Label = steadyStateCurrentLabel
		state.Escrows = append(state.Escrows, regular)
	}
	for index := range steadyStateReserves {
		standby := fullEscrow("standby-"+strconv.Itoa(index), steadyStateSpareFree)
		standby.Standby = true
		state.Escrows = append(state.Escrows, standby)
	}
	state.Escrows = append(state.Escrows, extra)
	state.TargetCount, state.ReserveCount, state.TempCount, state.MaxUnsettled = steadyStateTarget, steadyStateReserves, steadyStateTemps, steadyStateMaxUnsettled
	state.CurrentLabel, state.DemandWindowFull = steadyStateCurrentLabel, true
	return state
}

func applyDecision(state ModelState, decision Decision, tick int) ModelState {
	next := state
	next.Escrows = nil
	for _, escrow := range state.Escrows {
		retired := false
		for _, retire := range decision.Retires {
			retired = retired || retire.EscrowID == escrow.ID
		}
		if !retired {
			next.Escrows = append(next.Escrows, escrow)
		}
	}
	for index, create := range decision.Creates {
		created := fullEscrow("created-"+strconv.Itoa(tick)+"-"+strconv.Itoa(index), steadyStateSpareFree)
		created.Label = state.CurrentLabel
		created.Standby = create.Standby
		next.Escrows = append(next.Escrows, created)
	}
	next.Counted = len(next.Escrows)
	next.SurplusStreak = 0
	if decision.SurplusHeld {
		next.SurplusStreak = state.SurplusStreak + 1
	}
	return next
}

// Test flow:
//  1. Build a model at its target of current-label regulars with a full escrow beside them that the spread count leaves out, a temp or an older-label regular, and the reserves held.
//  2. Plan sixteen scheduled ticks, applying each decision to the next tick's state as the manager would.
//  3. Assert no tick retires or creates anything: the model is at steady state.
func TestAModelAtItsTargetReachesASteadyStateBesideAnEscrowSpreadDoesNotCount(t *testing.T) {
	t.Parallel()
	temp := fullEscrow("temp", steadyStateSpareFree)
	temp.Temp = true
	older := fullEscrow("older", steadyStateSpareFree)
	older.Label = steadyStateOlderLabel
	testCases := []struct {
		name  string
		extra EscrowState
	}{
		{name: "a temp escrow", extra: temp},
		{name: "an older-label regular escrow", extra: older},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			state := steadyStateModel(testCase.extra)
			for tick := range steadyStateTicks {
				decision := Plan(state)
				require.Empty(t, decision.Retires, "tick %d retires", tick)
				require.Empty(t, decision.Creates, "tick %d creates", tick)
				state = applyDecision(state, decision, tick)
			}
		})
	}
}
