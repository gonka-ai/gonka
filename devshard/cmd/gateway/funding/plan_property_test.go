package funding

import (
	"math/rand/v2"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/cmd/gateway/liquidity"
)

const (
	propertySeeds        = 16
	statesPerSeed        = 500
	surplusStatesPerSeed = 200
	propertyFullCost     = 32_778
	propertyMaxEscrow    = 9
)

func randomModelState(random *rand.Rand) ModelState {
	escrows := make([]EscrowState, random.IntN(propertyMaxEscrow))
	for index := range escrows {
		full := random.IntN(2) == 0
		capped := random.IntN(10) == 0
		escrows[index] = EscrowState{
			ID: strconv.Itoa(index), Standby: random.IntN(5) == 0, Temp: random.IntN(5) == 0,
			Label: int64(6 + random.IntN(2)), NonceCapReached: capped, FullCost: propertyFullCost,
			IdleFor: time.Duration(random.IntN(20)) * time.Minute,
			Money: liquidity.Escrow{
				Free: random.Uint64N(2_000_000), Returning: random.Uint64N(100_000),
				Full: full, Starved: !full && !capped, Idle: random.IntN(2) == 0,
			},
		}
	}
	return ModelState{
		Escrows: escrows, Counted: len(escrows) + random.IntN(4),
		TargetCount: 1 + random.IntN(4), TempCount: random.IntN(3), ReserveCount: random.IntN(3),
		MaxUnsettled: random.IntN(14), FullContextSlots: 1 + random.IntN(3),
		Amount: random.Uint64N(2_000_000), CreateFee: 100, FullCost: propertyFullCost, Slot: 2 * propertyFullCost,
		Priced: random.IntN(10) != 0, DemandPeak: random.Uint64N(4_000_000), DemandWindowFull: random.IntN(2) == 0,
		SurplusStreak: random.IntN(4), ScheduledTick: random.IntN(2) == 0, Parking: random.IntN(3), MoneyShort: random.IntN(3) == 0, WalletShort: random.IntN(4) == 0,
		RequestsBlocked: random.IntN(6) == 0, EpochKnown: random.IntN(8) != 0, ServedByNetwork: random.IntN(8) != 0,
		BreakerGated: random.IntN(6) == 0, InBridgeWindow: random.IntN(6) == 0, NearBridgeWindow: random.IntN(4) == 0,
		CurrentLabel: 7, BucketTokens: random.IntN(3),
	}
}

// randomSurplusState draws a model with ample budget, a full demand window and K within two of its full count.
func randomSurplusState(random *rand.Rand) ModelState {
	escrows := make([]EscrowState, 4+random.IntN(9))
	fullEscrows := 0
	for index := range escrows {
		full := index == 0 || random.IntN(10) < 7
		free := random.Uint64N(60_000)
		if full {
			fullEscrows++
			free = 100_000 + random.Uint64N(900_000)
		}
		escrows[index] = EscrowState{
			ID: strconv.Itoa(index), Standby: index > 0 && random.IntN(10) == 0, Temp: random.IntN(8) == 0,
			Label: 7, FullCost: propertyFullCost, IdleFor: time.Duration(random.IntN(12)) * time.Minute,
			Money: liquidity.Escrow{Free: free, Returning: random.Uint64N(20_000), Full: full, Starved: !full, Idle: random.IntN(2) == 0},
		}
	}
	counted := len(escrows) + random.IntN(2)
	return ModelState{
		Escrows: escrows, Counted: counted,
		TargetCount: 1 + random.IntN(2), TempCount: random.IntN(2), ReserveCount: random.IntN(2),
		MaxUnsettled: counted + 3 + random.IntN(6), FullContextSlots: max(1, fullEscrows-random.IntN(3)),
		Amount: 1_000_000, CreateFee: 100, FullCost: propertyFullCost, Slot: 2 * propertyFullCost,
		Priced: true, DemandPeak: random.Uint64N(200_001), DemandWindowFull: true,
		SurplusStreak: 2 + random.IntN(2), ScheduledTick: random.IntN(2) == 0, Parking: random.IntN(2),
		EpochKnown: true, ServedByNetwork: true, InBridgeWindow: random.IntN(6) == 0, NearBridgeWindow: random.IntN(4) == 0,
		CurrentLabel: 7, BucketTokens: random.IntN(3),
	}
}

func isRoutable(escrow EscrowState) bool {
	return !escrow.Standby && !escrow.NonceCapReached
}

// expectedRoom is the model's room computed apart from Plan.
func expectedRoom(model ModelState) int {
	room := model.MaxUnsettled - model.Counted
	if model.NearBridgeWindow && !model.InBridgeWindow {
		room -= model.TempCount
	}
	return room
}

// expectedPlan holds the planner's terms computed from the README formulas apart from Plan.
type expectedPlan struct {
	room             int
	fullEscrows      int
	need             uint64
	liquid           uint64
	currentRegulars  int
	misconfigured    bool
	shortfalls       Shortfalls
	regularWanted    int
	routableFullFree uint64
	surplusHeld      bool
}

// computeExpectedPlan derives every term from the state alone; the generators keep all sums far below overflow.
func computeExpectedPlan(model ModelState) expectedPlan {
	expected := expectedPlan{room: expectedRoom(model)}
	expected.misconfigured = model.Priced && (model.Amount <= model.CreateFee || model.Amount-model.CreateFee < model.Slot)
	expected.need = model.DemandPeak + model.DemandPeak/2
	if !expected.misconfigured {
		expected.need += uint64(model.FullContextSlots) * model.Slot
	}
	routableCount, currentStandbys := 0, 0
	for _, escrow := range model.Escrows {
		if escrow.Money.Full && !escrow.NonceCapReached {
			expected.fullEscrows++
		}
		if escrow.Standby && !escrow.NonceCapReached && escrow.Label >= model.CurrentLabel {
			currentStandbys++
		}
		if !isRoutable(escrow) {
			continue
		}
		routableCount++
		expected.liquid += escrow.Money.Returning
		if escrow.Money.Free > escrow.FullCost {
			expected.liquid += escrow.Money.Free - escrow.FullCost
		}
		if escrow.Money.Full {
			expected.routableFullFree += escrow.Money.Free
		}
		if !escrow.Temp && escrow.Label == model.CurrentLabel {
			expected.currentRegulars++
		}
	}
	if model.Priced && !expected.misconfigured {
		expected.shortfalls.Guard = max(0, model.FullContextSlots-expected.fullEscrows)
	}
	perCreate := uint64(0)
	if model.Amount > model.CreateFee+model.FullCost {
		perCreate = model.Amount - model.CreateFee - model.FullCost
	}
	if model.Priced && (model.MoneyShort || expected.need > expected.liquid) && perCreate > 0 {
		gap := uint64(1)
		if expected.need > expected.liquid+1 {
			gap = expected.need - expected.liquid
		}
		expected.shortfalls.Capacity = int(min((gap+perCreate-1)/perCreate, uint64(model.MaxUnsettled)+1))
	}
	expected.shortfalls.Spread = max(0, model.TargetCount-expected.currentRegulars)
	expected.shortfalls.Standby = max(0, model.ReserveCount-currentStandbys)
	expected.regularWanted = max(expected.shortfalls.Guard, expected.shortfalls.Capacity, expected.shortfalls.Spread)
	if model.Priced && model.DemandWindowFull && model.Amount > model.FullCost && expected.regularWanted == 0 {
		perEscrow := model.Amount - model.FullCost
		uncovered := uint64(0)
		if expected.need > expected.routableFullFree {
			uncovered = expected.need - expected.routableFullFree
		}
		wanted := model.FullContextSlots + int((uncovered+perEscrow-1)/perEscrow)
		expected.surplusHeld = routableCount > max(model.TargetCount, wanted)
	}
	return expected
}

// withStandbyFree returns the state with every standby escrow's free money replaced.
func withStandbyFree(model ModelState, free uint64) ModelState {
	changed := model
	changed.Escrows = slices.Clone(model.Escrows)
	for index := range changed.Escrows {
		if changed.Escrows[index].Standby {
			changed.Escrows[index].Money.Free = free
		}
	}
	return changed
}

// checkPlanProperties plans one state, asserts every property on it and returns the number of surplus retires.
func checkPlanProperties(t *testing.T, random *rand.Rand, model ModelState, where string) int {
	t.Helper()
	decision := Plan(model)
	expected := computeExpectedPlan(model)
	allowed := !model.RequestsBlocked && model.EpochKnown && !model.BreakerGated && model.ServedByNetwork && !model.InBridgeWindow

	require.Equal(t, expected.room, decision.Room, "%s: room", where)
	require.Equal(t, expected.fullEscrows, decision.FullCount, "%s: full count", where)
	require.Equal(t, expected.misconfigured, decision.Misconfigured, "%s: misconfigured", where)
	require.Equal(t, expected.shortfalls, decision.Shortfalls, "%s: shortfalls", where)
	require.Equal(t, expected.surplusHeld, decision.SurplusHeld, "%s: surplus held", where)
	require.Equal(t, expected.need, decision.Need, "%s: need", where)
	require.Equal(t, expected.liquid, decision.Liquid, "%s: liquid", where)
	require.LessOrEqual(t, len(decision.Creates), max(expected.room, 0), "%s: creates over the room", where)
	require.LessOrEqual(t, len(decision.Creates), model.BucketTokens, "%s: creates over the bucket", where)
	if allowed {
		require.Len(t, decision.Creates, min(expected.regularWanted+expected.shortfalls.Standby, max(expected.room, 0), max(model.BucketTokens, 0)), "%s: creates not sized by the shortfalls, room and bucket", where)
	} else {
		require.Empty(t, decision.Creates, "%s: a create while it is not allowed", where)
	}
	if expected.shortfalls == (Shortfalls{}) {
		require.Empty(t, decision.Creates, "%s: a create with no shortfall", where)
	}
	require.Equal(t, regularCreates(decision.Creates) < expected.shortfalls.Guard || (model.WalletShort && expected.shortfalls.Guard > 0), decision.Broken, "%s: broken", where)
	for _, create := range decision.Creates {
		switch create.Reason {
		case ReasonGuard:
			require.Less(t, expected.fullEscrows, model.FullContextSlots, "%s: a guard create with K full escrows", where)
		case ReasonCapacity:
			require.True(t, model.MoneyShort || expected.need > expected.liquid, "%s: a capacity create with money enough", where)
		case ReasonSpread:
			require.Less(t, expected.currentRegulars, model.TargetCount, "%s: a spread create with the spread met", where)
		}
	}

	planned, removedFull, surplusRetires := 0, 0, 0
	for _, retire := range decision.Retires {
		if retire.Reason == ReasonNonceCap {
			continue
		}
		planned++
		retired := escrowByID(model.Escrows, retire.EscrowID)
		switch retire.Reason {
		case ReasonSurplus:
			surplusRetires++
			require.Zero(t, expected.regularWanted, "%s: a surplus retire beside a wanted regular create", where)
			held := model.SurplusStreak
			if model.ScheduledTick {
				held++
			}
			require.GreaterOrEqual(t, held, surplusSamples, "%s: a surplus retire before three scheduled samples", where)
			if retired.Money.Starved {
				require.Greater(t, expected.fullEscrows, model.FullContextSlots, "%s: a starved surplus retire without a spare full escrow", where)
			}
		case ReasonBudgetPressure:
			walletPressed := model.WalletShort && expected.regularWanted > 0 && model.Parking == 0
			require.True(t, expected.regularWanted-expected.room > model.Parking || walletPressed, "%s: budget pressure while parking covers the deficit and the wallet is not short", where)
			require.True(t, retired.Money.Starved, "%s: budget pressure parked an escrow that is not starved", where)
		}
		if retired.Money.Full {
			removedFull++
			require.GreaterOrEqual(t, expected.fullEscrows, model.FullContextSlots+2, "%s: a full escrow retired without two spares", where)
		}
	}
	require.LessOrEqual(t, planned, 1, "%s: more than one planned retire", where)
	if model.InBridgeWindow {
		require.Zero(t, planned, "%s: a planned retire inside the bridge window", where)
	}
	require.GreaterOrEqual(t, expected.fullEscrows-removedFull, min(model.FullContextSlots, expected.fullEscrows), "%s: the full count taken below K", where)

	require.Equal(t, decision, Plan(withStandbyFree(model, 0)), "%s: an empty standby changed the decision", where)
	require.Equal(t, decision, Plan(withStandbyFree(model, 5_000_000)), "%s: a rich standby changed the decision", where)
	shuffled := model
	shuffled.Escrows = slices.Clone(model.Escrows)
	random.Shuffle(len(shuffled.Escrows), func(left, right int) {
		shuffled.Escrows[left], shuffled.Escrows[right] = shuffled.Escrows[right], shuffled.Escrows[left]
	})
	require.Equal(t, decision, Plan(shuffled), "%s: the decision depends on the escrow order", where)
	require.Equal(t, decision, Plan(model), "%s: the same input gave another decision", where)
	return surplusRetires
}

func escrowByID(escrows []EscrowState, escrowID string) EscrowState {
	for _, escrow := range escrows {
		if escrow.ID == escrowID {
			return escrow
		}
	}
	return EscrowState{}
}

// Test flow:
//  1. For sixteen fixed seeds, generate five hundred random model states each.
//  2. Plan every state, and plan it again with its escrows shuffled.
//  3. Assert every property of the design against terms computed apart from Plan: room, full count, misconfiguration, shortfalls and surplus as the README formulas give them; creates within the room and the bucket and, when allowed, exactly the shortfalls bounded by both; none while blocked, without an epoch, while the breaker is gated, the model is not served or inside the bridge window; none when every shortfall is zero; broken exactly when regular creates fall below the guard; every create reason true of the state; at most one planned retire; surplus only after three scheduled samples, never beside a wanted regular create, and of a starved escrow only above K full; budget pressure only past the parking escrows or for a short wallet with nothing parking, and only of a starved escrow; no planned retire inside the bridge window; the need and liquid money the decision carries; a planned retire never takes the full count below K and removes a full escrow only with two spares; standby money never moves the decision; the same input gives the same decision in any escrow order.
func TestPlanKeepsEveryPropertyOverRandomModelStates(t *testing.T) {
	t.Parallel()
	surplusRetires := 0
	for seed := uint64(1); seed <= propertySeeds; seed++ {
		random := rand.New(rand.NewPCG(seed, 0))
		for state := range statesPerSeed {
			surplusRetires += checkPlanProperties(t, random, randomModelState(random), "seed "+strconv.FormatUint(seed, 10)+" state "+strconv.Itoa(state))
		}
	}
	t.Logf("surplus retires over %d random states: %d", propertySeeds*statesPerSeed, surplusRetires)
}

// Test flow:
//  1. For sixteen fixed seeds, generate two hundred model states each with ample budget, a full demand window and K within two of the full count.
//  2. Plan every state and assert every property as above.
//  3. Assert the pass planned surplus retires at all.
func TestPlanKeepsEveryPropertyOverSurplusModelStates(t *testing.T) {
	t.Parallel()
	surplusRetires := 0
	for seed := uint64(1); seed <= propertySeeds; seed++ {
		random := rand.New(rand.NewPCG(seed, 1))
		for state := range surplusStatesPerSeed {
			surplusRetires += checkPlanProperties(t, random, randomSurplusState(random), "surplus seed "+strconv.FormatUint(seed, 10)+" state "+strconv.Itoa(state))
		}
	}
	t.Logf("surplus retires over %d surplus states: %d", propertySeeds*surplusStatesPerSeed, surplusRetires)
	require.Positive(t, surplusRetires, "surplus retires")
}
