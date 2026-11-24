package funding

import (
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/cmd/gateway/liquidity"
)

const (
	simulatedTickInterval = 15 * time.Second
	simulatedTicks        = 10
)

type simulatedTick struct {
	creates []Create
	retires []Retire
}

type landingCreate struct {
	escrowID string
	standby  bool
}

type simulatedModel struct {
	state          ModelState
	bucket         Bucket
	now            time.Time
	surplusStreak  int
	walletEmpty    bool
	walletMarked   bool
	landingDelay   int
	nextID         int
	pendingBatches [][]landingCreate
	createdIDs     map[string]bool
}

func newSimulatedModel(state ModelState) *simulatedModel {
	now := time.Date(2026, 11, 17, 22, 0, 0, 0, time.UTC)
	return &simulatedModel{state: state, bucket: NewBucket(now), now: now, createdIDs: map[string]bool{}}
}

func (model *simulatedModel) freshEscrow(escrowID string, standby bool) EscrowState {
	return EscrowState{
		ID: escrowID, Standby: standby, Unread: true, Label: model.state.CurrentLabel, FullCost: model.state.FullCost,
		Money: liquidity.Classify(liquidity.Input{Balance: model.state.Amount, Price: liquidity.Price{Slot: model.state.Slot, Priced: model.state.Priced}}),
	}
}

func (model *simulatedModel) readEveryEscrow() {
	for index := range model.state.Escrows {
		model.state.Escrows[index].Unread = false
	}
}

func (model *simulatedModel) tick() simulatedTick {
	model.now = model.now.Add(simulatedTickInterval)
	var outcome simulatedTick
	model.readEveryEscrow()
	model.landDueCommitments(&outcome)
	model.bucket.Refill(model.now, simulatedTickInterval)
	model.state.BucketTokens, model.state.SurplusStreak = model.bucket.Available(), model.surplusStreak
	model.state.ScheduledTick, model.state.DemandWindowFull = true, true
	model.state.WalletShort = model.walletMarked
	decision := Plan(model.state)
	if decision.Shortfalls.Guard+decision.Shortfalls.Capacity+decision.Shortfalls.Spread == 0 {
		model.walletMarked = false
	}
	model.bucket.Take(len(decision.Creates))
	if decision.SurplusHeld {
		model.surplusStreak++
	} else {
		model.surplusStreak = 0
	}
	outcome.creates, outcome.retires = decision.Creates, decision.Retires
	model.applyRetires(decision.Retires)
	model.applyCreates(decision.Creates)
	return outcome
}

func (model *simulatedModel) applyRetires(retires []Retire) {
	for _, retire := range retires {
		for index, escrow := range model.state.Escrows {
			if escrow.ID == retire.EscrowID {
				model.state.Escrows = append(model.state.Escrows[:index:index], model.state.Escrows[index+1:]...)
				model.state.Parking++
				break
			}
		}
	}
}

func (model *simulatedModel) applyCreates(creates []Create) {
	var batch []landingCreate
	for _, create := range creates {
		if model.walletEmpty {
			model.walletMarked = true
			break
		}
		model.walletMarked = false
		model.nextID++
		landing := landingCreate{escrowID: "created-" + strconv.Itoa(model.nextID), standby: create.Standby}
		model.createdIDs[landing.escrowID] = true
		model.state.Counted++
		if landing.standby {
			model.state.PendingStandbyCreates++
		} else {
			model.state.PendingRegularCreates++
		}
		batch = append(batch, landing)
	}
	model.pendingBatches = append(model.pendingBatches, batch)
}

func (model *simulatedModel) landDueCommitments(outcome *simulatedTick) {
	if len(model.pendingBatches) <= model.landingDelay {
		return
	}
	due := model.pendingBatches[0]
	model.pendingBatches = model.pendingBatches[1:]
	for _, landing := range due {
		if landing.standby {
			model.state.PendingStandbyCreates--
		} else {
			model.state.PendingRegularCreates--
		}
		model.state.Escrows = append(model.state.Escrows, model.freshEscrow(landing.escrowID, landing.standby))
	}
}

func (model *simulatedModel) run(ticks int) []simulatedTick {
	history := make([]simulatedTick, 0, ticks)
	for range ticks {
		history = append(history, model.tick())
	}
	return history
}

func countCreates(history []simulatedTick) (regulars, standbys int) {
	for _, outcome := range history {
		for _, create := range outcome.creates {
			if create.Standby {
				standbys++
			} else {
				regulars++
			}
		}
	}
	return regulars, standbys
}

func retiredIDs(history []simulatedTick) []string {
	var retired []string
	for _, outcome := range history {
		for _, retire := range outcome.retires {
			retired = append(retired, retire.EscrowID)
		}
	}
	return retired
}

func standbysOf(state ModelState) int {
	count := 0
	for _, escrow := range state.Escrows {
		if escrow.Standby {
			count++
		}
	}
	return count
}

func requireNoCarousel(t *testing.T, model *simulatedModel, history []simulatedTick) {
	t.Helper()
	for _, escrowID := range retiredIDs(history) {
		require.Falsef(t, model.createdIDs[escrowID], "escrow %s was created by the planner and then retired by it", escrowID)
	}
	require.LessOrEqual(t, model.state.Counted, model.state.MaxUnsettled, "counted rows against max_unsettled")
}

// Test flow:
//  1. Start from the production GLM state, with each create staying a commitment for one more tick and then landing as an unread row, and the surplus streak advancing as the manager advances it.
//  2. Plan ten ticks, applying every create to the state the way the manager does.
//  3. Assert exactly one regular guard escrow was created in the first tick and nothing after, the standby count stays at reserve_count, and the planner retired nothing, least of all what it created, though the surplus rule holds from the second tick on.
func TestAFundedGuardStopsTheCreatesOnTheNextTick(t *testing.T) {
	t.Parallel()
	model := newSimulatedModel(fragmentedGLM())
	model.landingDelay = 1

	history := model.run(simulatedTicks)

	regulars, standbys := countCreates(history)
	require.Equal(t, 1, regulars, "regular creates over %d ticks", simulatedTicks)
	require.Zero(t, standbys, "standby creates over %d ticks", simulatedTicks)
	require.Equal(t, creates(ReasonGuard), history[0].creates)
	require.Empty(t, retiredIDs(history))
	require.Equal(t, model.state.ReserveCount, standbysOf(model.state), "standbys held")
	require.Equal(t, model.state.FullContextSlots, Plan(model.state).FullCount, "routable full escrows at the end")
	require.True(t, Plan(model.state).SurplusHeld, "surplus held at the end")
	require.GreaterOrEqual(t, model.surplusStreak, surplusSamples, "surplus streak")
	requireNoCarousel(t, model, history)
}

// Test flow:
//  1. Start from the production GLM state with three full-context slots wanted, so two guard escrows are short, and a bucket that allows one create per tick; each create stays a commitment for one more tick.
//  2. Plan ten ticks, applying every create to the state.
//  3. Assert one guard create in each of the first two ticks and none after, and exactly three routable full escrows at the end.
func TestAGuardShortfallBeyondTheBucketIsFilledOnePerTickAndNoMore(t *testing.T) {
	t.Parallel()
	state := fragmentedGLM()
	state.FullContextSlots = 3
	model := newSimulatedModel(state)
	model.bucket = Bucket{refilledAt: model.now}
	model.landingDelay = 1

	history := model.run(simulatedTicks)

	require.Equal(t, creates(ReasonGuard), history[0].creates)
	require.Equal(t, creates(ReasonGuard), history[1].creates)
	regulars, standbys := countCreates(history)
	require.Equal(t, state.FullContextSlots-1, regulars, "regular creates over %d ticks", simulatedTicks)
	require.Zero(t, standbys, "standby creates")
	require.Equal(t, state.FullContextSlots, Plan(model.state).FullCount, "routable full escrows at the end")
	require.Empty(t, retiredIDs(history))
	requireNoCarousel(t, model, history)
}

// Test flow:
//  1. Start from a model with two full regulars, no standby and reserve_count 1; each create stays a commitment for one more tick and then lands as an unread row.
//  2. Plan ten ticks, applying every create to the state.
//  3. Assert one standby was created in the first tick, none after, and nothing was retired.
func TestAFundedStandbyStopsTheCreatesOnTheNextTick(t *testing.T) {
	t.Parallel()
	state := healthyModel()
	state.ReserveCount, state.CurrentLabel, state.Amount, state.Slot, state.FullCost = 1, 7, 2_000_000, 2*testFullCost, testFullCost
	model := newSimulatedModel(state)
	model.landingDelay = 1

	history := model.run(simulatedTicks)

	regulars, standbys := countCreates(history)
	require.Zero(t, regulars, "regular creates")
	require.Equal(t, 1, standbys, "standby creates over %d ticks", simulatedTicks)
	require.Equal(t, 1, standbysOf(model.state), "standbys held")
	require.Empty(t, retiredIDs(history))
	requireNoCarousel(t, model, history)
}

// Test flow:
//  1. Start from a model with reserve_count 1, no standby and a wallet that refuses every create, so nothing ever lands.
//  2. Plan ten ticks.
//  3. Assert no tick asks for more than the one standby reserve_count names, no regular escrow is asked for, the row count never moves and nothing is retired.
//  4. Top the wallet up, plan ten more ticks, and assert one standby lands and the creates stop.
func TestAnUnderfundedWalletAsksForOneStandbyAtMostPerTick(t *testing.T) {
	t.Parallel()
	state := healthyModel()
	state.ReserveCount, state.Amount, state.Slot, state.FullCost = 1, 2_000_000, 2*testFullCost, testFullCost
	model := newSimulatedModel(state)
	model.walletEmpty = true
	rows := len(model.state.Escrows)

	history := model.run(simulatedTicks)

	for tickNumber, outcome := range history {
		require.LessOrEqualf(t, len(outcome.creates), state.ReserveCount, "creates asked for at tick %d", tickNumber)
		for _, create := range outcome.creates {
			require.Truef(t, create.Standby, "tick %d asked for a regular escrow", tickNumber)
		}
	}
	require.Len(t, model.state.Escrows, rows, "rows while the wallet refuses")
	require.Equal(t, rows, model.state.Counted, "counted rows while the wallet refuses")
	require.Empty(t, retiredIDs(history))

	model.walletEmpty = false
	history = model.run(simulatedTicks)

	regulars, standbys := countCreates(history)
	require.Zero(t, regulars, "regular creates after the top-up")
	require.Equal(t, 1, standbys, "standby creates after the top-up")
	require.Equal(t, 1, standbysOf(model.state), "standbys held")
	require.Empty(t, retiredIDs(history))
	requireNoCarousel(t, model, history)
}
