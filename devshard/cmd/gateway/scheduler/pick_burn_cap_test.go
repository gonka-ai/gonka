package scheduler

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/config"
	"devshard/types"
)

const (
	pickCapGroupSize   = 8
	pickCapBurnedSlots = 5
	pickCapLimit       = 3
	pickCapSmallLimit  = 2
	pickCapForcedAfter = 2
	pickCapThinBalance = 15_000
	pickCapFullBalance = 1 << 30
	pickCapWait        = 2 * time.Second
	pickCapMaxNonce    = 100_000
)

func withBurnCap(scheduler *Scheduler, limit int64) {
	configuration := config.Defaults()
	configuration.Scheduler.MaxBurnsPerRequest = limit
	scheduler.settings = &config.Holder{}
	scheduler.settings.Swap(&configuration)
}

func blockSlots(scheduler *Scheduler, escrowID string, first, count int) {
	slots := slotsOf(escrowID, pickCapGroupSize)
	for _, slot := range slots[first : first+count] {
		scheduler.perf.(*fakePerf).eject(slot)
	}
}

func chargedTally(burns int) *BurnTally {
	tally := &BurnTally{}
	for range burns {
		tally.Charge()
	}
	return tally
}

// Test flow:
//  1. Build two escrows whose next five slots from the cursor are ejected hosts, with a cap of three burns per request.
//  2. Pick for a request carrying a fresh tally.
//  3. Assert the pick answers `ErrRequestCapped` rather than no capacity, since every escrow forecasts more burns than the request has left.
//  4. Charge a tally two burns under a cap of six and assert the pick is refused, since four are left for a forecast of five, then charge a tally one burn and assert an escrow is picked, since five are left.
func TestEveryEscrowForecastingMoreBurnsThanRemainAnswersCappedAtPick(t *testing.T) {
	scheduler, _, _ := newScheduler(
		candidate{id: "first", weight: 1, groupSize: pickCapGroupSize},
		candidate{id: "second", weight: 1, groupSize: pickCapGroupSize},
	)
	blockSlots(scheduler, "first", firstBurnSlot, pickCapBurnedSlots)
	blockSlots(scheduler, "second", firstBurnSlot, pickCapBurnedSlots)

	withBurnCap(scheduler, pickCapLimit)
	_, err := pickWith(scheduler, RequestProfile{Model: modelA, Burns: &BurnTally{}}, chain.PhaseSnapshot{})
	require.ErrorIs(t, err, ErrRequestCapped)
	require.NotErrorIs(t, err, ErrNoEscrowCapacity)

	withBurnCap(scheduler, pickCapBurnedSlots+1)
	_, err = pickWith(scheduler, RequestProfile{Model: modelA, Burns: chargedTally(2)}, chain.PhaseSnapshot{})
	require.ErrorIs(t, err, ErrRequestCapped)
	picked, err := pickWith(scheduler, RequestProfile{Model: modelA, Burns: chargedTally(1)}, chain.PhaseSnapshot{})
	require.NoError(t, err)
	require.NotEmpty(t, picked.ID)
}

// Test flow:
//  1. Build a funded escrow whose next five slots are ejected hosts and a thin escrow that serves on its next nonce under a heavier load, with a cap of three burns.
//  2. Pick for a request carrying a fresh tally and assert the thin escrow is picked, since the funded one forecasts more burns than the request has left and so leaves the tiers.
//  3. Pick for a request with no tally and assert the funded escrow wins its tier, since without a tally nothing is filtered.
func TestTheEscrowThatFitsTheRemainingCapIsPickedWhateverItsTierAndLoad(t *testing.T) {
	scheduler, _, _ := newScheduler(
		candidate{id: "funded", weight: 1, groupSize: pickCapGroupSize, balance: pickCapFullBalance, tokenPrice: reserveTestPrice},
		candidate{id: "thin", weight: 1, activeUsers: 40, groupSize: pickCapGroupSize, balance: pickCapThinBalance, tokenPrice: reserveTestPrice},
	)
	blockSlots(scheduler, "funded", firstBurnSlot, pickCapBurnedSlots)
	withBurnCap(scheduler, pickCapLimit)
	profile := RequestProfile{Model: modelA, InputBytes: reserveTestSmallBytes}

	capped := profile
	capped.Burns = &BurnTally{}
	picked, err := pickWith(scheduler, capped, chain.PhaseSnapshot{})
	require.NoError(t, err)
	require.Equal(t, "thin", picked.ID)

	untallied, err := pickWith(scheduler, profile, chain.PhaseSnapshot{})
	require.NoError(t, err)
	require.Equal(t, "funded", untallied.ID)
}

// Test flow:
//  1. Build an escrow whose next five slots from the cursor are ejected hosts, with a cap of three burns.
//  2. Pick for a request carrying a fresh tally that excludes those five hosts and assert the escrow is picked, since the drain charges the request none of the burns on hosts it excluded.
//  3. Pick for a request that excludes only the first of them and assert it is refused with `ErrRequestCapped`, since the four burns left are its own and pass the cap.
func TestThePickDoesNotRefuseARequestOnBurnsItIsNotChargedFor(t *testing.T) {
	scheduler, _, _ := newScheduler(candidate{id: "first", weight: 1, groupSize: pickCapGroupSize})
	blockSlots(scheduler, "first", firstBurnSlot, pickCapBurnedSlots)
	withBurnCap(scheduler, pickCapLimit)
	burned := slotsOf("first", pickCapGroupSize)[firstBurnSlot : firstBurnSlot+pickCapBurnedSlots]

	picked, err := pickWith(scheduler, RequestProfile{Model: modelA, Exclude: burned, Burns: &BurnTally{}}, chain.PhaseSnapshot{})
	require.NoError(t, err)
	require.Equal(t, "first", picked.ID)

	_, err = pickWith(scheduler, RequestProfile{Model: modelA, Exclude: burned[:1], Burns: &BurnTally{}}, chain.PhaseSnapshot{})
	require.ErrorIs(t, err, ErrRequestCapped)
}

// Test flow:
//  1. Build two escrows whose next five slots are ejected hosts, and a cap of zero.
//  2. Pick for a request carrying a tally that has already burned a hundred nonces.
//  3. Assert an escrow is picked, since a cap of zero filters nothing.
func TestACapOfZeroFiltersNoEscrowAtPick(t *testing.T) {
	scheduler, _, _ := newScheduler(
		candidate{id: "first", weight: 1, groupSize: pickCapGroupSize},
		candidate{id: "second", weight: 1, groupSize: pickCapGroupSize},
	)
	blockSlots(scheduler, "first", firstBurnSlot, pickCapBurnedSlots)
	blockSlots(scheduler, "second", firstBurnSlot, pickCapBurnedSlots)
	withBurnCap(scheduler, BurnCapOff)

	picked, err := pickWith(scheduler, RequestProfile{Model: modelA, Burns: chargedTally(100)}, chain.PhaseSnapshot{})

	require.NoError(t, err)
	require.NotEmpty(t, picked.ID)
}

// Test flow:
//  1. Build an escrow that serves on its next nonce and a cap of three burns.
//  2. Pick for a request whose tally has already burned a hundred nonces, past the cap.
//  3. Assert the escrow is picked, since a serve burns nothing and the cap only refuses burns.
func TestAnEscrowThatServesOnItsNextNonceIsNeverDroppedForTheCap(t *testing.T) {
	scheduler, _, _ := newScheduler(candidate{id: "open", weight: 1, groupSize: pickCapGroupSize})
	withBurnCap(scheduler, pickCapLimit)

	picked, err := pickWith(scheduler, RequestProfile{Model: modelA, Burns: chargedTally(100)}, chain.PhaseSnapshot{})

	require.NoError(t, err)
	require.Equal(t, "open", picked.ID)
}

// Test flow:
//  1. Build an escrow whose every slot is an ejected host and a cap of two burns, so the forecast is one lap of eight burns and no slot can take the request.
//  2. Pick for a request carrying a fresh tally.
//  3. Assert the escrow is still picked, since the drain's sweep answers a group nothing can serve without a nonce and the pick leaves that answer to it.
func TestAnEscrowNoSlotOfWhichCanTakeTheRequestIsLeftToTheSweep(t *testing.T) {
	scheduler, _, _ := newScheduler(candidate{id: "busy", weight: 1, groupSize: pickCapGroupSize})
	blockSlots(scheduler, "busy", 0, pickCapGroupSize)
	withBurnCap(scheduler, pickCapSmallLimit)

	picked, err := pickWith(scheduler, RequestProfile{Model: modelA, Burns: &BurnTally{}}, chain.PhaseSnapshot{})

	require.NoError(t, err)
	require.Equal(t, "busy", picked.ID)
}

// Test flow:
//  1. Build a funded escrow whose next five slots are ejected hosts and a full reserve that serves on its next nonce, with a cap of three burns.
//  2. Pick for a request carrying a fresh tally.
//  3. Assert the pick answers `ErrRequestCapped` and takes no reserve, since an escrow passed over for its hosts does not open the reserves.
func TestARegularPassedOverForTheBurnCapDoesNotOpenTheReserve(t *testing.T) {
	scheduler, recorder := schedulerWithReserve(
		candidate{id: "regular", weight: 1, groupSize: pickCapGroupSize},
		candidate{id: "reserve", weight: 1, groupSize: pickCapGroupSize, isReserve: true},
	)
	blockSlots(scheduler, "regular", firstBurnSlot, pickCapBurnedSlots)
	withBurnCap(scheduler, pickCapLimit)

	_, err := pickWith(scheduler, RequestProfile{Model: modelA, Burns: &BurnTally{}}, chain.PhaseSnapshot{})

	require.ErrorIs(t, err, ErrRequestCapped)
	require.Empty(t, recorder.recorded())
}

// Test flow:
//  1. Build an escrow whose next five slots are ejected hosts and pin escalating requests to it, with a cap of three burns.
//  2. Pick for an escalation whose tally has burned nothing and assert it is refused with `ErrRequestCapped`.
//  3. Raise the cap to five and assert the same pick returns the pinned escrow, then pick without a tally and assert the pinned escrow is returned.
func TestAPinnedEscalationIsRefusedWhenItsWalkCostsMoreBurnsThanRemain(t *testing.T) {
	scheduler, _, _ := newScheduler(candidate{id: "pinned", weight: 1, groupSize: pickCapGroupSize})
	blockSlots(scheduler, "pinned", firstBurnSlot, pickCapBurnedSlots)
	profile := RequestProfile{Model: modelA, Escrow: "pinned", Escalation: true, Burns: &BurnTally{}}

	withBurnCap(scheduler, pickCapLimit)
	_, err := pickWith(scheduler, profile, chain.PhaseSnapshot{})
	require.ErrorIs(t, err, ErrRequestCapped)

	withBurnCap(scheduler, pickCapBurnedSlots)
	picked, err := pickWith(scheduler, profile, chain.PhaseSnapshot{})
	require.NoError(t, err)
	require.Equal(t, "pinned", picked.ID)

	profile.Burns = nil
	withBurnCap(scheduler, pickCapLimit)
	picked, err = pickWith(scheduler, profile, chain.PhaseSnapshot{})
	require.NoError(t, err)
	require.Equal(t, "pinned", picked.ID)
}

// Test flow:
//  1. Build an escrow retired at its nonce cap, held only by a race, whose next five slots are ejected hosts, with a cap of three burns.
//  2. Pick for an escalation of that escrow carrying a fresh tally, under a snapshot that lets the escalation past the cutoff.
//  3. Assert the pick is refused with `ErrRequestCapped`, and that with a cap of five the retired escrow is returned.
func TestAnEscalationOfARetiredEscrowIsRefusedWhenItsWalkCostsMoreBurnsThanRemain(t *testing.T) {
	scheduler, escrows, _ := newScheduler(candidate{id: "other", weight: 1, groupSize: pickCapGroupSize})
	retired := Escrow{ID: "retired", Model: modelA, Session: &fakeSession{slots: slotsOf("retired", pickCapGroupSize)}}
	escrows.retired = map[string]Escrow{"retired": retired}
	blockSlots(scheduler, "retired", firstBurnSlot, pickCapBurnedSlots)
	snapshot := chain.PhaseSnapshot{MaxNonce: pickCapMaxNonce}
	profile := RequestProfile{Model: modelA, Escrow: "retired", Escalation: true, Burns: &BurnTally{}}

	withBurnCap(scheduler, pickCapLimit)
	_, err := pickWith(scheduler, profile, snapshot)
	require.ErrorIs(t, err, ErrRequestCapped)

	withBurnCap(scheduler, pickCapBurnedSlots)
	picked, err := pickWith(scheduler, profile, snapshot)
	require.NoError(t, err)
	require.Equal(t, "retired", picked.ID)
}

// Test flow:
//  1. Walk a group whose first five slots are full-window hosts with `expectedBurns`, once with no forced send and once with a forced send after two burns.
//  2. Assert the first walk prices five burns and the second two, since the drain sends over the third full window.
//  3. Put a shadow slot ahead of the full-window hosts and assert the forced walk prices one burn, since the drain's probe on the shadow slot already counts toward the run.
//  4. Walk a group of full-window hosts alone with a forced send and assert it is priced at its lap with no slot to serve, since the sweep answers such a group before any nonce is bound.
func TestTheForecastModelsTheForcedSendOverARunOfFullWindows(t *testing.T) {
	group := []string{"host-0", "host-1", "host-2", "host-3", "host-4", "host-5", "host-6", "host-7"}
	queued := newWaiter(RequestProfile{Model: modelA}, time.Time{})
	window := forecastGates("host-1", "host-2", "host-3", "host-4", "host-5")

	plain := expectedBurns(forecastEscrow(0, group...), window, queued, 0, 0)
	require.Equal(t, 5, plain.burns)
	require.True(t, plain.usable)
	forced := expectedBurns(forecastEscrow(0, group...), window, queued, 0, pickCapForcedAfter)
	require.Equal(t, 2, forced.burns)
	require.True(t, forced.usable)

	shadowed := forecastGates("host-2", "host-3", "host-4", "host-5")
	shadowed.shadowed = func(participant string) bool { return participant == "host-1" }
	probed := expectedBurns(forecastEscrow(0, group...), shadowed, queued, 0, pickCapForcedAfter)
	require.Equal(t, 1, probed.burns)
	require.True(t, probed.usable)

	every := forecastGates(group...)
	lap := expectedBurns(forecastEscrow(0, group...), every, queued, 0, pickCapForcedAfter)
	require.Equal(t, len(group), lap.burns)
	require.False(t, lap.usable)
}

// Test flow:
//  1. Build two escrows, the first ranking best with no burns and the second needing five, with a cap of three burns, and make the first escrow's session fail with `types.ErrInsufficientBalance`.
//  2. Pick for a request carrying a fresh tally, so the first round routes to the first escrow and its drain reports it out of funds.
//  3. Assert the second round, which leaves out the first escrow and finds the second over the cap, answers `ErrRequestCapped` rather than a shard out of funds, and that no nonce was burned.
func TestAnOutOfFundsEscrowFollowedByACappedOneAnswersCapped(t *testing.T) {
	group := groupOf(pickCapGroupSize)
	test := newSchedulerHarness(t, schedulerConfig{
		escrows:       []string{escrowA, escrowB},
		slotsByEscrow: map[string][]string{escrowA: group, escrowB: slotsOf(escrowB, pickCapGroupSize)},
	})
	test.session(t, escrowA).failWith = types.ErrInsufficientBalance
	next := *test.settings.Load()
	next.Scheduler.MaxBurnsPerRequest = pickCapLimit
	test.settings.Swap(&next)
	test.weights.byEscrow[escrowB] = 1
	for _, host := range slotsOf(escrowB, pickCapGroupSize)[firstBurnSlot : firstBurnSlot+pickCapBurnedSlots] {
		test.perf.eject(host)
	}

	_, err := test.scheduler.Pick(t.Context(), RequestProfile{Model: modelA, Params: "payload", Burns: &BurnTally{}})

	require.ErrorIs(t, err, ErrRequestCapped)
	require.NotErrorIs(t, err, types.ErrInsufficientBalance)
	require.Empty(t, test.observer.burns())
}

// Test flow:
//  1. Build a scheduler harness over one eight-host escrow whose session holds the drain at its first advance, with a cap of three burns.
//  2. Pick for a request carrying a fresh tally in a goroutine, wait until the drain is held, which is after the pick forecast no burn, then eject the next five slots and release the drain.
//  3. Assert the pick is answered `ErrRequestCapped` after exactly three burns and three committed nonces, since the drain-time check still stops what the forecast could not see.
func TestTheDrainStillCapsBurnsThatAppearAfterThePick(t *testing.T) {
	group := groupOf(pickCapGroupSize)
	gate := make(chan struct{})
	test := newSchedulerHarness(t, schedulerConfig{slots: group, gate: gate})
	next := *test.settings.Load()
	next.Scheduler.MaxBurnsPerRequest = pickCapLimit
	test.settings.Swap(&next)
	answered := make(chan error, 1)
	go func() {
		_, err := test.scheduler.Pick(t.Context(), RequestProfile{Model: modelA, Params: "payload", Burns: &BurnTally{}})
		answered <- err
	}()

	select {
	case <-test.session(t, escrowA).entered:
	case <-time.After(pickCapWait):
		t.Fatal("timed out waiting for the drain to reach its first advance")
	}
	for _, host := range group[firstBurnSlot : firstBurnSlot+pickCapBurnedSlots] {
		test.perf.eject(host)
	}
	close(gate)

	select {
	case err := <-answered:
		require.ErrorIs(t, err, ErrRequestCapped)
	case <-time.After(pickCapWait):
		t.Fatal("timed out waiting for the capped answer")
	}
	require.Len(t, test.observer.burns(), pickCapLimit)
	_, _, commits := test.session(t, escrowA).report()
	require.Len(t, commits, pickCapLimit, "the nonce past the cap is not committed")
}

// Test flow:
//  1. Build a scheduler harness over one eight-host escrow, and serve a first attempt of a request at the first slot with a cap of three burns.
//  2. Make the next five slots ejected and pick a hedge pinned to the escrow that shares the first attempt's tally.
//  3. Assert the hedge pick is refused with `ErrRequestCapped`, no nonce was burned or committed for it, and the first attempt's assignment is intact to finish.
func TestAHedgeThePickRefusesLeavesTheFirstAttemptsAssignmentIntact(t *testing.T) {
	group := groupOf(pickCapGroupSize)
	test := newSchedulerHarness(t, schedulerConfig{slots: group})
	next := *test.settings.Load()
	next.Scheduler.MaxBurnsPerRequest = pickCapLimit
	test.settings.Swap(&next)
	tally := &BurnTally{}

	first, err := test.scheduler.Pick(t.Context(), RequestProfile{Model: modelA, Params: "payload", Burns: tally})
	wantHost(t, first, err, escrowA, group[firstBurnSlot], firstBurnSlot)
	for _, host := range group[firstBurnSlot+1 : firstBurnSlot+1+pickCapBurnedSlots] {
		test.perf.eject(host)
	}
	_, err = test.scheduler.Pick(t.Context(), RequestProfile{Model: modelA, Params: "payload", Escrow: escrowA, Escalation: true, Burns: tally})

	require.ErrorIs(t, err, ErrRequestCapped)
	require.Empty(t, test.observer.burns())
	_, _, commits := test.session(t, escrowA).report()
	require.Len(t, commits, 1, "only the first attempt's nonce is committed")
	require.NotNil(t, first.Nonce)
}
