package funding

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/cmd/gateway/liquidity"
)

const testFullCost = 32_778

func fullEscrow(escrowID string, free uint64) EscrowState {
	return EscrowState{ID: escrowID, Label: 7, FullCost: testFullCost, Money: liquidity.Escrow{Free: free, Full: true, Idle: true}}
}

func starvedEscrow(escrowID string, free uint64) EscrowState {
	return EscrowState{ID: escrowID, Label: 7, FullCost: testFullCost, Money: liquidity.Escrow{Free: free, Starved: true, Idle: true}}
}

func pricedAt(escrow EscrowState, fullCost uint64) EscrowState {
	escrow.FullCost = fullCost
	return escrow
}

func busy(escrow EscrowState) EscrowState {
	escrow.Money.Idle = false
	return escrow
}

func idleFor(escrow EscrowState, duration time.Duration) EscrowState {
	escrow.IdleFor = duration
	return escrow
}

func unread(escrow EscrowState) EscrowState {
	escrow.Unread = true
	return escrow
}

func nonceCapped(escrow EscrowState) EscrowState {
	escrow.NonceCapReached = true
	return escrow
}

// healthyModel holds two full escrows of a target-2 model whose guarantee wants two: nothing to create, nothing to retire.
func healthyModel() ModelState {
	return ModelState{
		Escrows:          []EscrowState{fullEscrow("1", 1_000_000), fullEscrow("2", 1_000_000)},
		Counted:          2,
		TargetCount:      2,
		TempCount:        1,
		MaxUnsettled:     7,
		FullContextSlots: 2,
		Amount:           1_000_000,
		CreateFee:        100,
		FullCost:         testFullCost,
		Slot:             2 * testFullCost,
		Priced:           true,
		EpochKnown:       true,
		ServedByNetwork:  true,
		CurrentLabel:     7,
		BucketTokens:     2,
		ScheduledTick:    true,
	}
}

func modelWith(change func(state *ModelState)) ModelState {
	state := healthyModel()
	change(&state)
	return state
}

func demandAboveLiquidity(state *ModelState) { state.DemandPeak = 2_000_000 }

func creates(reasons ...Reason) []Create {
	planned := make([]Create, 0, len(reasons))
	for _, reason := range reasons {
		planned = append(planned, Create{Standby: reason == ReasonStandby, Reason: reason})
	}
	return planned
}

// Test flow:
//  1. Table-driven: each case changes one input of a healthy two-escrow model — an escrow's money, demand, a signal, the budget, the bucket, a gate, a price, a create still landing.
//  2. Plan the model.
//  3. Assert the creates and their reasons, the planned retires, and the broken, misconfigured and budget flags the case expects.
func TestPlanSizesCreatesAndRetiresFromMoneyDemandAndBudget(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name              string
		state             ModelState
		wantCreates       []Create
		wantRetires       []Retire
		wantBroken        bool
		wantMisconfigured bool
		wantBudget        bool
	}{
		{name: "a healthy model wants nothing", state: healthyModel()},
		{
			name:        "one full escrow short of the guarantee funds a guard create",
			state:       modelWith(func(state *ModelState) { state.Escrows[1] = starvedEscrow("2", 40_000) }),
			wantCreates: creates(ReasonGuard),
		},
		{
			name: "an open create commitment covers a guard shortfall",
			state: modelWith(func(state *ModelState) {
				state.Escrows[1] = starvedEscrow("2", 40_000)
				state.PendingRegularCreates = 1
			}),
		},
		{
			name: "an open create commitment covers the guard, so the create still owed is named for the spread",
			state: modelWith(func(state *ModelState) {
				state.Escrows[1] = starvedEscrow("2", 40_000)
				state.TargetCount = 4
				state.PendingRegularCreates = 1
			}),
			wantCreates: creates(ReasonSpread),
		},
		{
			name: "an open create commitment covers a spread shortfall",
			state: modelWith(func(state *ModelState) {
				state.TargetCount = 3
				state.PendingRegularCreates = 1
			}),
		},
		{
			name: "an open reserve commitment covers a standby shortfall",
			state: modelWith(func(state *ModelState) {
				state.ReserveCount = 1
				state.PendingStandbyCreates = 1
			}),
		},
		{
			name:        "a money-short signal funds one capacity create",
			state:       modelWith(func(state *ModelState) { state.MoneyShort = true }),
			wantCreates: creates(ReasonCapacity),
		},
		{
			name:        "demand above liquidity sizes the capacity creates",
			state:       modelWith(demandAboveLiquidity),
			wantCreates: creates(ReasonCapacity, ReasonCapacity),
		},
		{
			name:        "a spread shortfall funds a spread create",
			state:       modelWith(func(state *ModelState) { state.TargetCount = 3 }),
			wantCreates: creates(ReasonSpread),
		},
		{
			name:        "a standby is created after the regulars",
			state:       modelWith(func(state *ModelState) { state.ReserveCount, state.MoneyShort = 1, true }),
			wantCreates: creates(ReasonCapacity, ReasonStandby),
		},
		{
			name:        "the bucket bounds the creates",
			state:       modelWith(func(state *ModelState) { demandAboveLiquidity(state); state.BucketTokens = 1 }),
			wantCreates: creates(ReasonCapacity),
		},
		{
			name:        "the budget bounds the creates and is reported",
			state:       modelWith(func(state *ModelState) { demandAboveLiquidity(state); state.MaxUnsettled = 3 }),
			wantCreates: creates(ReasonCapacity),
			wantBudget:  true,
		},
		{
			name: "room near the bridge window is kept for its temps",
			state: modelWith(func(state *ModelState) {
				demandAboveLiquidity(state)
				state.MaxUnsettled, state.NearBridgeWindow = 4, true
			}),
			wantCreates: creates(ReasonCapacity),
			wantBudget:  true,
		},
		{
			name: "inside the bridge window the planner creates nothing",
			state: modelWith(func(state *ModelState) {
				demandAboveLiquidity(state)
				state.MaxUnsettled, state.NearBridgeWindow, state.InBridgeWindow = 4, true, true
			}),
		},
		{
			name: "blocked requests create nothing and break the guarantee",
			state: modelWith(func(state *ModelState) {
				state.Escrows[1] = starvedEscrow("2", 40_000)
				state.RequestsBlocked = true
			}),
			wantBroken: true,
		},
		{name: "no epoch creates nothing", state: modelWith(func(state *ModelState) { state.MoneyShort, state.EpochKnown = true, false })},
		{name: "a gated breaker creates nothing", state: modelWith(func(state *ModelState) { state.MoneyShort, state.BreakerGated = true, true })},
		{name: "a model the network does not serve creates nothing", state: modelWith(func(state *ModelState) { state.MoneyShort, state.ServedByNetwork = true, false })},
		{
			name: "a misconfigured model has no guarantee",
			state: modelWith(func(state *ModelState) {
				state.Escrows[1] = starvedEscrow("2", 40_000)
				state.Amount = state.CreateFee + state.Slot - 1
			}),
			wantMisconfigured: true,
		},
		{
			name: "an amount that pays one slot is configured",
			state: modelWith(func(state *ModelState) {
				state.Escrows[1] = starvedEscrow("2", 40_000)
				state.Amount = state.CreateFee + state.Slot + 1_000
			}),
			wantCreates: creates(ReasonGuard),
		},
		{
			name: "escrows priced before a price rise count their money at the current price",
			state: modelWith(func(state *ModelState) {
				state.Escrows = []EscrowState{pricedAt(fullEscrow("1", 50_000), testFullCost/20), pricedAt(fullEscrow("2", 50_000), testFullCost/20)}
			}),
		},
		{
			name: "escrows priced before a price fall count their money at the current price",
			state: modelWith(func(state *ModelState) {
				state.Escrows = []EscrowState{pricedAt(fullEscrow("1", 140_000), 2*testFullCost), pricedAt(fullEscrow("2", 140_000), 2*testFullCost)}
			}),
			wantCreates: creates(ReasonCapacity),
		},
		{
			name:              "an amount under the create fee funds no capacity",
			state:             modelWith(func(state *ModelState) { state.MoneyShort, state.Amount = true, 50 }),
			wantMisconfigured: true,
		},
		{
			name:        "an unpriced model only spreads",
			state:       modelWith(func(state *ModelState) { state.Priced, state.TargetCount, state.MoneyShort = false, 3, true }),
			wantCreates: creates(ReasonSpread),
		},
		{
			name: "extreme amounts saturate instead of wrapping",
			state: modelWith(func(state *ModelState) {
				state.Amount, state.DemandPeak, state.Slot, state.MoneyShort = math.MaxUint64, math.MaxUint64, math.MaxUint64/2, true
			}),
			wantCreates: creates(ReasonCapacity),
		},
		{
			name: "nonce-capped escrows retire at once beside an idle starved one",
			state: modelWith(func(state *ModelState) {
				state.Escrows = []EscrowState{
					nonceCapped(fullEscrow("1", 1_000_000)),
					nonceCapped(fullEscrow("2", 1_000_000)),
					idleFor(starvedEscrow("3", 40_000), 20*time.Minute),
				}
				state.Counted = 3
			}),
			wantCreates: creates(ReasonGuard, ReasonGuard),
			wantRetires: []Retire{{EscrowID: "1", Reason: ReasonNonceCap}, {EscrowID: "2", Reason: ReasonNonceCap}, {EscrowID: "3", Reason: ReasonIdleStarved}},
		},
		{
			name: "a starved escrow idle under ten minutes stays",
			state: modelWith(func(state *ModelState) {
				state.Escrows = append(state.Escrows, idleFor(starvedEscrow("3", 40_000), 10*time.Minute-time.Second))
				state.Counted = 3
			}),
		},
		{
			name: "a starved escrow idle for ten minutes retires",
			state: modelWith(func(state *ModelState) {
				state.Escrows = append(state.Escrows, idleFor(starvedEscrow("3", 40_000), 10*time.Minute))
				state.Counted = 3
			}),
			wantRetires: []Retire{{EscrowID: "3", Reason: ReasonIdleStarved}},
		},
		{
			name: "a busy starved escrow is not idle",
			state: modelWith(func(state *ModelState) {
				state.Escrows = append(state.Escrows, busy(starvedEscrow("3", 40_000)))
				state.Counted = 3
			}),
		},
		{
			name: "budget pressure parks the least-free starved escrow, busy or not",
			state: modelWith(func(state *ModelState) {
				state.Escrows = append(state.Escrows, busy(starvedEscrow("3", 50_000)), busy(starvedEscrow("4", 40_000)))
				state.Counted, state.MaxUnsettled, state.MoneyShort = 4, 4, true
			}),
			wantRetires: []Retire{{EscrowID: "4", Reason: ReasonBudgetPressure}},
			wantBudget:  true,
		},
		{
			name: "budget pressure waits while the parking escrows cover the deficit",
			state: modelWith(func(state *ModelState) {
				state.Escrows = append(state.Escrows, busy(starvedEscrow("3", 50_000)), busy(starvedEscrow("4", 40_000)))
				state.Counted, state.MaxUnsettled, state.MoneyShort, state.Parking = 4, 4, true, 1
			}),
			wantBudget: true,
		},
		{
			name: "budget pressure parks when the deficit exceeds the parking escrows",
			state: modelWith(func(state *ModelState) {
				state.Escrows = append(state.Escrows, busy(starvedEscrow("3", 50_000)), busy(starvedEscrow("4", 40_000)))
				demandAboveLiquidity(state)
				state.Counted, state.MaxUnsettled, state.Parking = 4, 4, 1
			}),
			wantRetires: []Retire{{EscrowID: "4", Reason: ReasonBudgetPressure}},
			wantBudget:  true,
		},
		{
			name: "a missing standby alone never parks a serving escrow",
			state: modelWith(func(state *ModelState) {
				state.Escrows = append(state.Escrows, busy(starvedEscrow("3", 50_000)), busy(starvedEscrow("4", 40_000)))
				state.Counted, state.MaxUnsettled, state.ReserveCount = 4, 4, 1
			}),
			wantBudget: true,
		},
		{
			name: "surplus waits on a wakeup tick at the second sample",
			state: modelWith(func(state *ModelState) {
				state.Escrows = append(state.Escrows, fullEscrow("3", 1_000_000), starvedEscrow("4", 50_000))
				state.Counted, state.DemandWindowFull, state.SurplusStreak, state.ScheduledTick = 4, true, 2, false
			}),
		},
		{
			name: "surplus retires on a wakeup tick after three scheduled samples",
			state: modelWith(func(state *ModelState) {
				state.Escrows = append(state.Escrows, fullEscrow("3", 1_000_000), starvedEscrow("4", 50_000))
				state.Counted, state.DemandWindowFull, state.SurplusStreak, state.ScheduledTick = 4, true, 3, false
			}),
			wantRetires: []Retire{{EscrowID: "4", Reason: ReasonSurplus}},
		},
		{
			name: "a standby's money never makes a capacity-short model surplus",
			state: modelWith(func(state *ModelState) {
				standby := fullEscrow("S", 1_000_000)
				standby.Standby = true
				state.Escrows = []EscrowState{fullEscrow("A", 70_000), starvedEscrow("B", 10_000), starvedEscrow("C", 10_000), standby}
				state.Counted, state.FullContextSlots, state.TargetCount = 4, 1, 1
				state.DemandWindowFull, state.SurplusStreak = true, 2
			}),
			wantCreates: creates(ReasonCapacity),
		},
		{
			name: "surplus never parks the only current-label escrow while a spread create replaces it",
			state: modelWith(func(state *ModelState) {
				oldA, oldB := fullEscrow("A", 900_000), fullEscrow("B", 800_000)
				oldA.Label, oldB.Label = 6, 6
				state.Escrows = []EscrowState{oldA, oldB, fullEscrow("C", 200_000)}
				state.Counted, state.FullContextSlots, state.TargetCount = 3, 1, 2
				state.DemandWindowFull, state.SurplusStreak = true, 2
			}),
			wantCreates: creates(ReasonSpread),
		},
		{
			name: "surplus waits for a full demand window",
			state: modelWith(func(state *ModelState) {
				state.Escrows = append(state.Escrows, fullEscrow("3", 1_000_000), fullEscrow("4", 1_000_000))
				state.Counted, state.SurplusStreak = 4, 2
			}),
		},
		{
			name: "surplus retires the least-free starved escrow on the third sample",
			state: modelWith(func(state *ModelState) {
				state.Escrows = append(state.Escrows, fullEscrow("3", 1_000_000), starvedEscrow("4", 50_000))
				state.Counted, state.DemandWindowFull, state.SurplusStreak = 4, true, 2
			}),
			wantRetires: []Retire{{EscrowID: "4", Reason: ReasonSurplus}},
		},
		{
			name: "surplus waits on the second sample",
			state: modelWith(func(state *ModelState) {
				state.Escrows = append(state.Escrows, fullEscrow("3", 1_000_000), starvedEscrow("4", 50_000))
				state.Counted, state.DemandWindowFull, state.SurplusStreak = 4, true, 1
			}),
		},
		{
			name: "surplus keeps a full escrow while only one spare full escrow remains",
			state: modelWith(func(state *ModelState) {
				state.Escrows = []EscrowState{fullEscrow("1", 1_000_000), fullEscrow("2", 900_000), fullEscrow("3", 800_000)}
				state.Counted, state.DemandWindowFull, state.SurplusStreak = 3, true, 2
			}),
		},
		{
			name: "surplus retires the least-free full escrow with two spares",
			state: modelWith(func(state *ModelState) {
				state.Escrows = []EscrowState{fullEscrow("1", 1_000_000), fullEscrow("2", 900_000), fullEscrow("3", 800_000), fullEscrow("4", 700_000)}
				state.Counted, state.DemandWindowFull, state.SurplusStreak = 4, true, 2
			}),
			wantRetires: []Retire{{EscrowID: "4", Reason: ReasonSurplus}},
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			decision := Plan(testCase.state)
			require.Equal(t, testCase.wantCreates, decision.Creates, "creates")
			require.Equal(t, testCase.wantRetires, decision.Retires, "retires")
			require.Equal(t, testCase.wantBroken, decision.Broken, "broken")
			require.Equal(t, testCase.wantMisconfigured, decision.Misconfigured, "misconfigured")
			require.Equal(t, testCase.wantBudget, decision.BudgetReached, "budget reached")
		})
	}
}

// Test flow:
//  1. Plan a model one full escrow short that also lost a nonce-capped escrow.
//  2. Assert the decision names each reason once, creates first.
func TestADecisionNamesEachReasonOnceCreatesFirst(t *testing.T) {
	t.Parallel()
	state := modelWith(func(state *ModelState) {
		state.Escrows = []EscrowState{fullEscrow("1", 1_000_000), nonceCapped(fullEscrow("2", 1_000_000)), nonceCapped(fullEscrow("3", 1_000_000))}
		state.Counted = 3
	})

	require.Equal(t, []Reason{ReasonGuard, ReasonNonceCap}, Plan(state).Reasons())
}

func walletShortModel(change func(state *ModelState)) ModelState {
	return modelWith(func(state *ModelState) {
		state.Escrows = []EscrowState{fullEscrow("1", 1_000_000), busy(starvedEscrow("2", 40_000)), busy(starvedEscrow("3", 30_000))}
		state.Counted = 3
		state.WalletShort = true
		if change != nil {
			change(state)
		}
	})
}

// Test flow:
//  1. Table-driven: the bridge window, a full escrow at its nonce cap, and a wallet that refused the last create, each on a small model.
//  2. Plan the model.
//  3. Assert the creates, the retires and the broken flag the case expects.
func TestPlanUnderPlannerOnRules(t *testing.T) {
	t.Parallel()
	nonceCappedFull := nonceCapped(fullEscrow("3", 900_000))
	testCases := []struct {
		name        string
		state       ModelState
		wantCreates []Create
		wantRetires []Retire
		wantBroken  bool
	}{
		{
			name: "inside the bridge window a guard shortfall creates nothing and breaks the guarantee",
			state: modelWith(func(state *ModelState) {
				state.Escrows[1] = starvedEscrow("2", 40_000)
				state.InBridgeWindow = true
			}),
			wantBroken: true,
		},
		{
			name: "an unread escrow whose stored amount covers the slot counts toward the guarantee",
			state: modelWith(func(state *ModelState) {
				state.Escrows[1] = starvedEscrow("2", 40_000)
				state.Escrows = append(state.Escrows, unread(fullEscrow("3", 999_900)))
				state.Counted = 3
			}),
		},
		{
			name: "inside the bridge window an idle starved escrow is not retired",
			state: modelWith(func(state *ModelState) {
				state.Escrows = append(state.Escrows, idleFor(starvedEscrow("3", 40_000), 11*time.Minute))
				state.Counted, state.InBridgeWindow = 3, true
			}),
		},
		{
			name: "inside the bridge window a nonce-capped escrow is still retired",
			state: modelWith(func(state *ModelState) {
				state.Escrows = append(state.Escrows, nonceCapped(fullEscrow("3", 900_000)))
				state.Counted, state.InBridgeWindow = 3, true
			}),
			wantRetires: []Retire{{EscrowID: "3", Reason: ReasonNonceCap}},
		},
		{
			name: "a full escrow at its nonce cap is retired",
			state: modelWith(func(state *ModelState) {
				state.Escrows = append(state.Escrows, nonceCappedFull)
				state.Counted = 3
			}),
			wantRetires: []Retire{{EscrowID: "3", Reason: ReasonNonceCap}},
		},
		{
			name:        "a short wallet parks the least-free starved escrow beside the create it still plans, and breaks the guarantee",
			state:       walletShortModel(nil),
			wantCreates: creates(ReasonGuard),
			wantRetires: []Retire{{EscrowID: "3", Reason: ReasonBudgetPressure}},
			wantBroken:  true,
		},
		{
			name:        "a short wallet parks nothing while a park is pending",
			state:       walletShortModel(func(state *ModelState) { state.Parking = 1 }),
			wantCreates: creates(ReasonGuard),
			wantBroken:  true,
		},
		{
			name: "a short wallet parks nothing when no regular create is wanted",
			state: walletShortModel(func(state *ModelState) {
				state.Escrows = append(state.Escrows, fullEscrow("4", 1_000_000))
				state.Counted = 4
			}),
		},
		{
			name: "a misconfigured model sizes capacity from demand alone",
			state: modelWith(func(state *ModelState) {
				state.Escrows = []EscrowState{busy(starvedEscrow("1", 40_000)), busy(starvedEscrow("2", 40_000))}
				state.Amount = 60_000
			}),
		},
		{
			name: "a short wallet never parks a full escrow",
			state: walletShortModel(func(state *ModelState) {
				state.Escrows = []EscrowState{fullEscrow("1", 1_000_000), fullEscrow("2", 900_000)}
				state.Counted, state.FullContextSlots = 2, 3
			}),
			wantCreates: creates(ReasonGuard),
			wantBroken:  true,
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			decision := Plan(testCase.state)

			require.Equal(t, testCase.wantCreates, decision.Creates, "Plan(%s).Creates", testCase.name)
			require.Equal(t, testCase.wantRetires, decision.Retires, "Plan(%s).Retires", testCase.name)
			require.Equal(t, testCase.wantBroken, decision.Broken, "Plan(%s).Broken", testCase.name)
		})
	}
}

// Test flow:
//  1. Plan a healthy two-escrow model with no demand.
//  2. Assert the decision carries need = K × Slot and L = Σ (free − FullCost) over its two full escrows.
func TestADecisionCarriesTheNeedAndTheLiquidMoneyItWasSizedFrom(t *testing.T) {
	t.Parallel()
	decision := Plan(healthyModel())

	require.Equal(t, uint64(2*2*testFullCost), decision.Need, "Plan(healthy).Need")
	require.Equal(t, uint64(2*(1_000_000-testFullCost)), decision.Liquid, "Plan(healthy).Liquid")
}
