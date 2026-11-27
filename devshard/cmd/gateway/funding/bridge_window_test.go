package funding

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func temp(escrow EscrowState) EscrowState {
	escrow.Temp, escrow.Label = true, 8
	return escrow
}

func drainedBridge(change func(state *ModelState)) ModelState {
	return modelWith(func(state *ModelState) {
		state.Escrows = []EscrowState{temp(busy(starvedEscrow("1", 40_000))), temp(busy(starvedEscrow("2", 30_000)))}
		state.ActiveTemps, state.TempCount, state.MaxUnsettled, state.InBridgeWindow = 2, 2, 8, true
		if change != nil {
			change(state)
		}
	})
}

// Test flow:
//  1. Table-driven: a model inside the bridge window whose two temps are starved, each case changing one input — temp_count, the spread, the reserve, the budget, an open commitment, the bucket, a PoC, a gate.
//  2. Plan the model.
//  3. Assert the creates and their reasons, that nothing is retired, and the broken and budget flags the case expects.
func TestPlanFundsADrainedBridgeInsideTheWindow(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name        string
		state       ModelState
		wantCreates []Create
		wantBroken  bool
		wantBudget  bool
	}{
		{name: "two starved temps fund two guard creates", state: drainedBridge(nil), wantCreates: creates(ReasonGuard, ReasonGuard)},
		{
			name:        "the guard is held to temp_count",
			state:       drainedBridge(func(state *ModelState) { state.TempCount = 1 }),
			wantCreates: creates(ReasonGuard),
		},
		{
			name: "one full temp of a temp_count-1 model meets the guard",
			state: drainedBridge(func(state *ModelState) {
				state.TempCount = 1
				state.Escrows[0] = temp(fullEscrow("1", 1_000_000))
			}),
		},
		{
			name: "a capacity shortfall is funded beyond temp_count",
			state: drainedBridge(func(state *ModelState) {
				state.TempCount, state.DemandPeak = 1, 2_000_000
			}),
			wantCreates: creates(ReasonGuard, ReasonCapacity),
		},
		{
			name: "a spread shortfall and a standby shortfall create nothing",
			state: drainedBridge(func(state *ModelState) {
				state.Escrows = []EscrowState{temp(fullEscrow("1", 1_000_000)), temp(fullEscrow("2", 1_000_000))}
				state.TargetCount, state.ReserveCount, state.MaxUnsettled = 4, 2, 16
			}),
		},
		{
			name:        "the room keeps the next epoch's regulars and reserves",
			state:       drainedBridge(func(state *ModelState) { state.ReserveCount, state.MaxUnsettled = 1, 6 }),
			wantCreates: creates(ReasonGuard),
			wantBroken:  true,
			wantBudget:  true,
		},
		{
			name:        "rows still unsettled count once against the room",
			state:       drainedBridge(func(state *ModelState) { state.Counted, state.MaxUnsettled = 5, 6 }),
			wantCreates: creates(ReasonGuard),
			wantBroken:  true,
			wantBudget:  true,
		},
		{
			name:        "an open temp commitment covers one guard create",
			state:       drainedBridge(func(state *ModelState) { state.PendingRegularCreates, state.Counted = 1, 3 }),
			wantCreates: creates(ReasonGuard),
		},
		{
			name:       "an empty bucket creates nothing and breaks the guarantee",
			state:      drainedBridge(func(state *ModelState) { state.BucketTokens = 0 }),
			wantBroken: true,
		},
		{
			name:        "an epoch PoC that requests pass still funds",
			state:       drainedBridge(func(state *ModelState) { state.InEpochPoC = true }),
			wantCreates: creates(ReasonGuard, ReasonGuard),
		},
		{
			name:       "an epoch PoC that blocks requests creates nothing and breaks the guarantee",
			state:      drainedBridge(func(state *ModelState) { state.InEpochPoC, state.RequestsBlocked = true, true }),
			wantBroken: true,
		},
		{
			name:       "a gated breaker creates nothing and breaks the guarantee",
			state:      drainedBridge(func(state *ModelState) { state.BreakerGated = true }),
			wantBroken: true,
		},
		{
			name:       "a model the network does not serve creates nothing and breaks the guarantee",
			state:      drainedBridge(func(state *ModelState) { state.ServedByNetwork = false }),
			wantBroken: true,
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			decision := Plan(testCase.state)

			require.Equal(t, testCase.wantCreates, decision.Creates, "Plan(%s).Creates", testCase.name)
			require.Empty(t, decision.Retires, "Plan(%s).Retires", testCase.name)
			require.Equal(t, testCase.wantBroken, decision.Broken, "Plan(%s).Broken", testCase.name)
			require.Equal(t, testCase.wantBudget, decision.BudgetReached, "Plan(%s).BudgetReached", testCase.name)
		})
	}
}

// Test flow:
//  1. Build a model inside the bridge window whose two temps are starved, with one open temp commitment among three counted rows, a bucket of five and a demand peak that wants more creates than the room holds.
//  2. Plan the model.
//  3. Assert the room is three, the commitment counted as a temp beside the two rows, and exactly three creates were planned.
func TestPlanCountsAnOpenTempCommitmentTowardTheWindowRoom(t *testing.T) {
	t.Parallel()
	decision := Plan(drainedBridge(func(state *ModelState) {
		state.PendingRegularCreates, state.Counted, state.BucketTokens, state.DemandPeak = 1, 3, 5, 9_000_000
	}))

	require.Equal(t, 3, decision.Room, "Plan(drained bridge with an open temp commitment).Room")
	require.Equal(t, creates(ReasonGuard, ReasonCapacity, ReasonCapacity), decision.Creates, "Plan(drained bridge with an open temp commitment).Creates")
}
