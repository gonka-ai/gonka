package funding

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Test flow:
//  1. Build a model one full escrow short of its guarantee whose requests the chain blocks, once in a confirmation PoC and once in an epoch PoC.
//  2. Plan each, and plan the confirmation PoC again with a block that is neither.
//  3. Assert the confirmation PoC creates the guard escrow, the epoch PoC creates nothing, and a block of any other reason creates nothing.
func TestAConfirmationPoCAllowsCreatesWhileAnEpochPoCDoesNot(t *testing.T) {
	t.Parallel()
	short := func(change func(state *ModelState)) ModelState {
		return modelWith(func(state *ModelState) {
			state.Escrows = []EscrowState{fullEscrow("1", 1_000_000)}
			state.Counted, state.RequestsBlocked = 1, true
			change(state)
		})
	}
	require.Equal(t, creates(ReasonGuard), Plan(short(func(state *ModelState) { state.InConfirmationPoC = true })).Creates, "confirmation PoC")
	require.Empty(t, Plan(short(func(state *ModelState) { state.InEpochPoC = true })).Creates, "epoch PoC")
	require.Empty(t, Plan(short(func(*ModelState) {})).Creates, "a block of another reason")
}
