package funding

import (
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/cmd/gateway/liquidity"
)

const (
	oneGNK              = uint64(1_000_000_000)
	fragmentedSlot      = 1_100_000_000
	fragmentedAttempts  = 4
	fragmentedFullCost  = fragmentedSlot / fragmentedAttempts
	fragmentedCreateFee = 1_000_000
	toppedUpRegularID   = "regular-7"
	toppedUpBalance     = fragmentedSlot + oneGNK/10
)

var fragmentedRegularBalances = []uint64{oneGNK / 10, oneGNK / 5, 3 * oneGNK / 10, oneGNK / 2, 775_000_000, 1_028_000_000, 1_033_000_000, 1_160_000_000}

func escrowHolding(escrowID string, free uint64) EscrowState {
	return EscrowState{
		ID: escrowID, Label: 7, FullCost: fragmentedFullCost,
		Money: liquidity.Classify(liquidity.Input{Balance: free, Price: liquidity.Price{Slot: fragmentedSlot, Priced: true}}),
	}
}

func standbyHolding(escrowID string, free uint64) EscrowState {
	escrow := escrowHolding(escrowID, free)
	escrow.Standby = true
	return escrow
}

func fragmentedGLM() ModelState {
	escrows := make([]EscrowState, 0, len(fragmentedRegularBalances)+1)
	for index, free := range fragmentedRegularBalances {
		escrows = append(escrows, escrowHolding("regular-"+strconv.Itoa(index+1), free))
	}
	escrows = append(escrows, standbyHolding("standby", 5*oneGNK/2))
	return ModelState{
		Escrows: escrows, Counted: len(escrows), TargetCount: 8, TempCount: 3, ReserveCount: 1, MaxUnsettled: 200, FullContextSlots: 2,
		Amount: 5 * oneGNK / 2, CreateFee: fragmentedCreateFee, FullCost: fragmentedFullCost, Slot: fragmentedSlot, Priced: true,
		EpochKnown: true, ServedByNetwork: true, CurrentLabel: 7, BucketTokens: 2, ScheduledTick: true,
	}
}

// Test flow:
//  1. Build the production GLM state: eight regular escrows drained to fragments of 0.1 to 1.16 GNK and one full standby, a slot of 1.1 GNK, two full-context slots wanted.
//  2. Plan it and count the routable full escrows.
//  3. Assert exactly one regular is full, the standby is not counted, and the planner funds one guard escrow.
func TestAStandbyDoesNotStandInForAFullRegularEscrow(t *testing.T) {
	t.Parallel()
	state := fragmentedGLM()

	decision := Plan(state)

	require.Equal(t, creates(ReasonGuard), decision.Creates)
	require.Equal(t, 1, decision.FullCount, "routable full escrows")
	require.Equal(t, 1, decision.Shortfalls.Guard, "guard shortfall")
	require.Empty(t, decision.Retires)
	require.False(t, decision.Broken)
}

// Test flow:
//  1. Build the same state with a second regular escrow topped up to a full slot.
//  2. Plan it.
//  3. Assert two routable full escrows meet the guarantee, so nothing is created or retired.
func TestTwoRoutableFullEscrowsCreateNothing(t *testing.T) {
	t.Parallel()
	state := fragmentedGLM()
	for index, escrow := range state.Escrows {
		if escrow.ID == toppedUpRegularID {
			state.Escrows[index] = escrowHolding(toppedUpRegularID, toppedUpBalance)
		}
	}

	decision := Plan(state)

	require.Empty(t, decision.Creates)
	require.Empty(t, decision.Retires)
	require.Equal(t, 2, decision.FullCount, "routable full escrows")
	require.Zero(t, decision.Shortfalls.Guard)
}
