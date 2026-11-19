package scheduler

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"devshard/types"
)

// busyEscrowHarness stands two escrows side by side with groups of their own, weighted so the first one wins the pick.
func busyEscrowHarness(t *testing.T, busy, spare string) *schedulerHarness {
	t.Helper()
	test := newSchedulerHarness(t, schedulerConfig{
		escrows: []string{busy, spare},
		slotsByEscrow: map[string][]string{
			busy:  {busy + "-0", busy + "-1"},
			spare: {spare + "-0", spare + "-1"},
		},
	})
	test.weights.byEscrow[busy] = 1_000
	test.weights.byEscrow[spare] = 10
	test.loadEscrow(t, spare, 1)
	return test
}

// busyGroup fills every window of an escrow's group, so its drain answers ErrHostsBusy.
func (h *schedulerHarness) busyGroup(t *testing.T, escrowID string) {
	t.Helper()
	for _, participant := range h.session(t, escrowID).slots {
		h.limiter.block(participant)
	}
}

// rounds counts one snapshot per pick and one per drain reached.
func (h *schedulerHarness) rounds() int { return h.snapshots.fetches() }

// reached reports that a pick actually routed to this escrow.
func (h *schedulerHarness) reached(t *testing.T, escrowID string) bool {
	t.Helper()
	_, live := h.liveDispatchers()[escrowID]
	return live
}

// Test flow:
//  1. Build a `busyEscrowHarness` where `escrowA` outweighs `escrowB`, then fill `escrowA`'s whole group.
//  2. Pick an escrow for a request.
//  3. Assert the assignment lands on `escrowB`'s second host at nonce 1.
//  4. Assert the pick reached `escrowA` first, and `escrowA`'s session advanced no nonces giving up.
func TestARequestAnEscrowGivesUpOnIsOfferedToAnotherEscrow(t *testing.T) {
	test := busyEscrowHarness(t, escrowA, escrowB)
	test.busyGroup(t, escrowA)

	assignment, err := test.scheduler.Pick(context.Background(), RequestProfile{Model: modelA})

	wantHost(t, assignment, err, escrowB, escrowB+"-1", 1)
	if !test.reached(t, escrowA) {
		t.Fatal("the pick never reached the busy escrow, so nothing was re-picked")
	}
	if advances, _, _ := test.session(t, escrowA).report(); advances != 0 {
		t.Fatalf("the escrow that gave up advanced %d nonces, want 0: its sweep answers without one", advances)
	}
}

// retireUnderPick makes an escrow refuse every hold, as one retired after the pick routed a request to it does.
func (h *schedulerHarness) retireUnderPick(t *testing.T, escrowID string) {
	t.Helper()
	candidates := h.escrows.byModel[modelA]
	for index := range candidates {
		if candidates[index].ID == escrowID {
			candidates[index].Hold = func() (func(), bool) { return nil, false }
			return
		}
	}
	t.Fatalf("escrow %s is not a candidate", escrowID)
}

// Test flow:
//  1. Build a `busyEscrowHarness` where `escrowA` outweighs `escrowB`, and retire `escrowA` under the pick.
//  2. Pick an escrow for an unpinned request.
//  3. Assert the request is served by `escrowB`: the escrow went away, the request did nothing wrong, and no nonce was spent on it.
func TestARequestWhoseEscrowRetiresWhileItWaitsIsOfferedToAnotherEscrow(t *testing.T) {
	test := busyEscrowHarness(t, escrowA, escrowB)
	test.retireUnderPick(t, escrowA)

	assignment, err := test.scheduler.Pick(context.Background(), RequestProfile{Model: modelA})

	if err != nil {
		t.Fatalf("Pick: %v, want the request carried to %s", err, escrowB)
	}
	if assignment.Escrow != escrowB {
		t.Fatalf("assignment escrow = %q, want %q", assignment.Escrow, escrowB)
	}
	if !test.reached(t, escrowA) {
		t.Fatal("the pick never reached the retiring escrow, so nothing was re-picked")
	}
}

// Test flow:
//  1. Build a `busyEscrowHarness` where `escrowA` outweighs `escrowB`, then fill `escrowA`'s whole group.
//  2. Pick an escrow for a request.
//  3. Assert `escrowB` advanced exactly the one nonce it served with, so the second round excluded `escrowA` rather than retrying it.
func TestTheSecondRoundLeavesOutTheEscrowThatGaveUp(t *testing.T) {
	test := busyEscrowHarness(t, escrowA, escrowB)
	test.busyGroup(t, escrowA)

	if _, err := test.scheduler.Pick(context.Background(), RequestProfile{Model: modelA}); err != nil {
		t.Fatalf("Pick: %v", err)
	}

	if advances, _, _ := test.session(t, escrowB).report(); advances != 1 {
		t.Fatalf("the spare escrow advanced %d nonces, want the one it served with", advances)
	}
}

// Test flow:
//  1. Build a `busyEscrowHarness` and make `escrowA` fail advancing with `types.ErrInsufficientBalance`.
//  2. Pick an escrow for a request.
//  3. Assert the pick succeeds and the assignment carries the escrow to `escrowB`.
func TestARequestAnEscrowCannotPayForIsOfferedToAnotherEscrow(t *testing.T) {
	test := busyEscrowHarness(t, escrowA, escrowB)
	test.sessions[escrowA].failAdvancing(types.ErrInsufficientBalance)

	assignment, err := test.scheduler.Pick(context.Background(), RequestProfile{Model: modelA})

	if err != nil {
		t.Fatalf("Pick: %v", err)
	}
	if assignment.Escrow != escrowB {
		t.Fatalf("assignment escrow = %q, want the request carried to %q", assignment.Escrow, escrowB)
	}
}

// Test flow:
//  1. Build a `busyEscrowHarness` and make `escrowA` fail advancing with a storage error of its own session.
//  2. Pick an escrow for a request.
//  3. Assert the pick succeeds on `escrowB`: a session that cannot advance is "this escrow, right now", and the request never chose it.
func TestARequestWhoseEscrowCannotAdvanceIsOfferedToAnotherEscrow(t *testing.T) {
	test := busyEscrowHarness(t, escrowA, escrowB)
	test.sessions[escrowA].failAdvancing(errors.New("append diff: database is locked"))

	assignment, err := test.scheduler.Pick(context.Background(), RequestProfile{Model: modelA})

	if err != nil {
		t.Fatalf("Pick: %v", err)
	}
	if assignment.Escrow != escrowB {
		t.Fatalf("assignment escrow = %q, want the request carried to %q", assignment.Escrow, escrowB)
	}
}

// Test flow:
//  1. Build a `busyEscrowHarness` and make both escrows fail advancing with a storage error of their own sessions.
//  2. Pick an escrow for a request.
//  3. Assert the pick fails with `ErrEscrowAdvanceFailed` and both escrows were reached: the second round's failure is the answer.
func TestTwoEscrowsThatCannotAdvanceAnswerTheAdvanceFailure(t *testing.T) {
	test := busyEscrowHarness(t, escrowA, escrowB)
	for _, escrowID := range []string{escrowA, escrowB} {
		test.sessions[escrowID].failAdvancing(errors.New("append diff: database is locked"))
	}

	_, err := test.scheduler.Pick(context.Background(), RequestProfile{Model: modelA})

	if !errors.Is(err, ErrEscrowAdvanceFailed) {
		t.Fatalf("Pick = %v, want ErrEscrowAdvanceFailed", err)
	}
	for _, escrowID := range []string{escrowA, escrowB} {
		if !test.reached(t, escrowID) {
			t.Fatalf("escrow %s was never asked", escrowID)
		}
	}
}

// Test flow:
//  1. Build a `busyEscrowHarness` and make both escrows fail advancing with `types.ErrInsufficientBalance`.
//  2. Pick an escrow for a request.
//  3. Assert the pick fails with `types.ErrInsufficientBalance`.
//  4. Assert both escrows were reached before the caller was refused.
func TestOnlyAFleetWithNoBalanceLeftRefusesTheCaller(t *testing.T) {
	test := busyEscrowHarness(t, escrowA, escrowB)
	for _, escrowID := range []string{escrowA, escrowB} {
		test.sessions[escrowID].failAdvancing(types.ErrInsufficientBalance)
	}

	_, err := test.scheduler.Pick(context.Background(), RequestProfile{Model: modelA})

	if !errors.Is(err, types.ErrInsufficientBalance) {
		t.Fatalf("Pick = %v, want the fleet's own out-of-funds answer", err)
	}
	for _, escrowID := range []string{escrowA, escrowB} {
		if !test.reached(t, escrowID) {
			t.Fatalf("escrow %s was never asked before the caller was refused", escrowID)
		}
	}
}

// Test flow:
//  1. Build a `busyEscrowHarness` and make both escrows fail advancing with `types.ErrInsufficientBalance`.
//  2. Pick an escrow for a request.
//  3. Assert the error is an `*EscrowsOutOfFundsError` reporting 2 escrows refused.
//  4. Assert it still matches `types.ErrInsufficientBalance` and `ErrNoEscrowCapacity`.
func TestAFundingRefusalNamesHowManyEscrowsWereAsked(t *testing.T) {
	test := busyEscrowHarness(t, escrowA, escrowB)
	for _, escrowID := range []string{escrowA, escrowB} {
		test.sessions[escrowID].failAdvancing(types.ErrInsufficientBalance)
	}

	_, err := test.scheduler.Pick(context.Background(), RequestProfile{Model: modelA})

	var refusal *EscrowsOutOfFundsError
	if !errors.As(err, &refusal) {
		t.Fatalf("Pick = %v, want a refusal that counts the escrows it asked", err)
	}
	if refusal.Refused != 2 {
		t.Fatalf("Refused = %d, want both escrows counted", refusal.Refused)
	}
	if !errors.Is(err, types.ErrInsufficientBalance) {
		t.Fatal("the count hid the fact the caller acts on")
	}
	if !errors.Is(err, ErrNoEscrowCapacity) {
		t.Fatal("the count hid the status the caller is answered with")
	}
}

// Test flow:
//  1. Build a `busyEscrowHarness` and make `escrowA` fail advancing with `types.ErrInsufficientBalance`.
//  2. Pick an escrow for a request.
//  3. Assert no escrow was reported exhausted, since one costly request is not a spent escrow.
func TestWalkingPastAnEscrowNeverAsksForItsReplacement(t *testing.T) {
	test := busyEscrowHarness(t, escrowA, escrowB)
	test.sessions[escrowA].failAdvancing(types.ErrInsufficientBalance)

	if _, err := test.scheduler.Pick(context.Background(), RequestProfile{Model: modelA}); err != nil {
		t.Fatalf("Pick: %v", err)
	}

	if reported := test.exhausted.all(); len(reported) != 0 {
		t.Fatalf("escrows reported exhausted = %v, want none: one costly request is not a spent escrow", reported)
	}
}

// Test flow:
//  1. Build a `busyEscrowHarness` and make `escrowA` fail advancing with `types.ErrInsufficientBalance`.
//  2. Pick an escrow for a request and assert the pick succeeds, carried to `escrowB`.
//  3. Clear `escrowA`'s failure.
//  4. Pick again and assert the assignment now lands back on `escrowA`.
func TestAnEscrowThatCouldNotPayForOneRequestServesTheNext(t *testing.T) {
	test := busyEscrowHarness(t, escrowA, escrowB)
	test.sessions[escrowA].failAdvancing(types.ErrInsufficientBalance)

	if _, err := test.scheduler.Pick(context.Background(), RequestProfile{Model: modelA}); err != nil {
		t.Fatalf("the oversized request was not carried to the spare escrow: %v", err)
	}
	test.sessions[escrowA].failAdvancing(nil)

	assignment, err := test.scheduler.Pick(context.Background(), RequestProfile{Model: modelA})

	if err != nil {
		t.Fatalf("Pick after the escrow recovered: %v", err)
	}
	if assignment.Escrow != escrowA {
		t.Fatalf("assignment escrow = %q, want %q back in service", assignment.Escrow, escrowA)
	}
}

// Test flow:
//  1. Build a `busyEscrowHarness` and fill both escrows' whole groups.
//  2. Pick an escrow for a request.
//  3. Assert the pick fails with `ErrHostsBusy`.
//  4. Assert 4 snapshot reads happened, the two rounds' worth, and no escrow advanced any nonce.
func TestABusyShardAnswersOnceAndStops(t *testing.T) {
	test := busyEscrowHarness(t, escrowA, escrowB)
	test.busyGroup(t, escrowA)
	test.busyGroup(t, escrowB)

	_, err := test.scheduler.Pick(context.Background(), RequestProfile{Model: modelA})

	if !errors.Is(err, ErrHostsBusy) {
		t.Fatalf("Pick = %v, want ErrHostsBusy", err)
	}
	if rounds := test.rounds(); rounds != 4 {
		t.Fatalf("a busy shard cost %d snapshot reads, want the two rounds' four", rounds)
	}
	for _, escrowID := range []string{escrowA, escrowB} {
		if !test.reached(t, escrowID) {
			t.Fatalf("escrow %s was never tried, so the one retry did not run", escrowID)
		}
		if advances, _, _ := test.session(t, escrowID).report(); advances != 0 {
			t.Fatalf("escrow %s advanced %d nonces answering a busy shard, want 0", escrowID, advances)
		}
	}
}

// Test flow:
//  1. Build a `busyEscrowHarness`, fill `escrowA`'s group, and block every host of `escrowB` outright.
//  2. Pick an escrow for a request.
//  3. Assert the error is `ErrHostsBusy`, the first escrow's own answer.
//  4. Assert the error is not `ErrNoAvailableHost`, the second escrow's worse one.
func TestAWorseRefusalFromTheSecondEscrowDoesNotReplaceTheBusyAnswer(t *testing.T) {
	test := busyEscrowHarness(t, escrowA, escrowB)
	test.busyGroup(t, escrowA)
	for _, participant := range test.session(t, escrowB).slots {
		test.scheduler.BlockHost(escrowB, participant)
	}

	_, err := test.scheduler.Pick(context.Background(), RequestProfile{Model: modelA})

	if !errors.Is(err, ErrHostsBusy) {
		t.Fatalf("Pick = %v, want the busy shard's own answer rather than the second escrow's worse one", err)
	}
	if errors.Is(err, ErrNoAvailableHost) {
		t.Fatalf("Pick = %v: a full shard must not answer as a broken one", err)
	}
}

// Test flow:
//  1. Build a `busyEscrowHarness`, fill `escrowA`'s group, and zero `escrowB`'s balance.
//  2. Pick an escrow for a request.
//  3. Assert the error is `types.ErrInsufficientBalance`, surviving the fold over the busy first escrow.
func TestASecondRoundOutOfFundsReachesTheCaller(t *testing.T) {
	test := busyEscrowHarness(t, escrowA, escrowB)
	test.busyGroup(t, escrowA)
	test.session(t, escrowB).balance = 0

	_, err := test.scheduler.Pick(context.Background(), RequestProfile{Model: modelA, OutputTokens: 64})

	if !errors.Is(err, types.ErrInsufficientBalance) {
		t.Fatalf("Pick = %v, want the out-of-funds answer to survive the fold", err)
	}
}

// Test flow:
//  1. Build a scheduler harness with one escrow and block both of its hosts.
//  2. Pick an escrow for a request.
//  3. Assert the error is `ErrHostsBusy`.
//  4. Assert the error is not `ErrNoEscrowCapacity`, since a merely busy shard must not read as a missing one.
func TestTheOnlyEscrowsAnswerSurvivesARePickWithNowhereToGo(t *testing.T) {
	test := newSchedulerHarness(t, schedulerConfig{escrows: []string{escrowA}})
	for _, participant := range []string{hostA, hostB} {
		test.limiter.block(participant)
	}

	_, err := test.scheduler.Pick(context.Background(), RequestProfile{Model: modelA})

	if !errors.Is(err, ErrHostsBusy) {
		t.Fatalf("Pick = %v, want the busy escrow's own answer", err)
	}
	if errors.Is(err, ErrNoEscrowCapacity) {
		t.Fatalf("Pick = %v, want a busy shard not to read as a missing one", err)
	}
}

// Test flow:
//  1. Build a `busyEscrowHarness` and fill `escrowA`'s whole group.
//  2. Pick pinned to `escrowA` for a request.
//  3. Assert the error is `ErrHostsBusy`, the pinned escrow's own answer.
//  4. Assert `escrowB` advanced no nonces and only one round's two snapshot reads happened, since a pinned pick is never re-picked.
func TestAPinnedEscrowIsNeverRePicked(t *testing.T) {
	test := busyEscrowHarness(t, escrowA, escrowB)
	test.busyGroup(t, escrowA)

	_, err := test.scheduler.Pick(context.Background(), RequestProfile{Model: modelA, Escrow: escrowA})

	if !errors.Is(err, ErrHostsBusy) {
		t.Fatalf("Pick = %v, want the pinned escrow's own answer", err)
	}
	if advances, _, _ := test.session(t, escrowB).report(); advances != 0 {
		t.Fatalf("a pinned pick reached escrow %s, which advanced %d nonces", escrowB, advances)
	}
	if rounds := test.rounds(); rounds != 2 {
		t.Fatalf("a pinned pick cost %d snapshot reads, want the one round's two", rounds)
	}
}

// Test flow:
//  1. Build a scheduler harness with `escrowA` outweighing `escrowB`, then block every host of `escrowA`'s group outright.
//  2. Pick an escrow for a request.
//  3. Assert the error is `ErrNoAvailableHost`.
//  4. Assert `escrowB` advanced no nonces, since a state-blocked refusal is not a condition another escrow gets a re-pick for.
func TestARefusalThatIsNotBusyIsAnsweredWhereItHappened(t *testing.T) {
	test := newSchedulerHarness(t, schedulerConfig{
		escrows: []string{escrowA, escrowB},
		slotsByEscrow: map[string][]string{
			escrowA: {escrowA + "-0", escrowA + "-1"},
			escrowB: {escrowB + "-0", escrowB + "-1"},
		},
	})
	test.weights.byEscrow[escrowA] = 1_000
	test.weights.byEscrow[escrowB] = 10
	test.loadEscrow(t, escrowB, 1)
	for _, participant := range test.session(t, escrowA).slots {
		test.scheduler.BlockHost(escrowA, participant)
	}

	_, err := test.scheduler.Pick(context.Background(), RequestProfile{Model: modelA})

	if !errors.Is(err, ErrNoAvailableHost) {
		t.Fatalf("Pick = %v, want ErrNoAvailableHost", err)
	}
	if advances, _, _ := test.session(t, escrowB).report(); advances != 0 {
		t.Fatalf("a state-blocked escrow re-picked onto %s, which advanced %d nonces", escrowB, advances)
	}
}

// Test flow:
//  1. Build a scheduler with one regular escrow whose whole group is busy and one reserve with hosts of its own.
//  2. Pick for a small request.
//  3. Assert the busy answer reaches the caller and the reserve is neither dispatched to nor taken: a reserve covers what no regular can pay for, not hosts that are busy.
func TestABusyRegularDoesNotSpendTheReserve(t *testing.T) {
	test := busyEscrowHarness(t, escrowA, escrowB)
	test.markReserve(escrowB)
	test.busyGroup(t, escrowA)

	_, err := test.scheduler.Pick(context.Background(), RequestProfile{Model: modelA})

	if !errors.Is(err, ErrHostsBusy) {
		t.Fatalf("Pick = %v, want ErrHostsBusy", err)
	}
	if test.reached(t, escrowB) {
		t.Fatal("the busy retry routed to the reserve")
	}
	if taken := test.reserves.recorded(); len(taken) != 0 {
		t.Fatalf("reserves taken = %v, want none", taken)
	}
}

// Test flow:
//  1. Build a scheduler with one regular escrow whose session fails advancing with a storage error and one reserve.
//  2. Pick for a request.
//  3. Assert the caller hears the advance failure rather than "no escrow capacity", and the reserve is neither dispatched to nor taken.
func TestAnAdvanceFailureWithNoOtherRegularKeepsItsOwnAnswer(t *testing.T) {
	test := busyEscrowHarness(t, escrowA, escrowB)
	test.markReserve(escrowB)
	test.sessions[escrowA].failAdvancing(errors.New("append diff: database is locked"))

	_, err := test.scheduler.Pick(context.Background(), RequestProfile{Model: modelA})

	if !errors.Is(err, ErrEscrowAdvanceFailed) {
		t.Fatalf("Pick = %v, want ErrEscrowAdvanceFailed", err)
	}
	if test.reached(t, escrowB) {
		t.Fatal("the advance failure routed to the reserve")
	}
	if taken := test.reserves.recorded(); len(taken) != 0 {
		t.Fatalf("reserves taken = %v, want none", taken)
	}
}

// Test flow:
//  1. Build a scheduler with one regular escrow whose session refuses for insufficient balance and one reserve.
//  2. Pick for a request.
//  3. Assert the request is served on the reserve and the take is reported: a regular that cannot pay is what the reserve is for.
func TestARegularThatCannotPayHandsTheRequestToTheReserve(t *testing.T) {
	test := busyEscrowHarness(t, escrowA, escrowB)
	test.markReserve(escrowB)
	test.session(t, escrowA).failWith = types.ErrInsufficientBalance

	assignment, err := test.scheduler.Pick(context.Background(), RequestProfile{Model: modelA})

	if err != nil || assignment.Escrow != escrowB {
		t.Fatalf("Pick = %+v, %v; want a nonce on the reserve", assignment, err)
	}
	if taken := test.reserves.recorded(); !slices.Equal(taken, []string{escrowB}) {
		t.Fatalf("reserves taken = %v, want [%s]", taken, escrowB)
	}
}

// gatedPicks holds the picks parked behind a shared session gate and releases them once.
type gatedPicks struct {
	gate     chan struct{}
	release  sync.Once
	inFlight sync.WaitGroup
}

func (g *gatedPicks) open() {
	g.release.Do(func() { close(g.gate) })
	g.inFlight.Wait()
}

// fullQueueHarness stands two gated escrows with submit buffers of one, weighted so the first one wins every pick it can take.
func fullQueueHarness(t *testing.T, preferred, spare string) (*schedulerHarness, *gatedPicks) {
	t.Helper()
	parked := &gatedPicks{gate: make(chan struct{})}
	test := newSchedulerHarness(t, schedulerConfig{
		escrows: []string{preferred, spare},
		slotsByEscrow: map[string][]string{
			preferred: {preferred + "-0", preferred + "-1"},
			spare:     {spare + "-0", spare + "-1"},
		},
		submitBuffer: 1,
		gate:         parked.gate,
	})
	t.Cleanup(parked.open)
	test.weights.byEscrow[preferred] = 1_000
	test.weights.byEscrow[spare] = 10
	test.loadEscrow(t, spare, 1)
	return test, parked
}

// fillQueue parks one pick in the escrow's session and one more in its submit buffer.
func (h *schedulerHarness) fillQueue(t *testing.T, escrowID string, parked *gatedPicks) {
	t.Helper()
	parkedPick := func() {
		if _, err := h.scheduler.Pick(context.Background(), RequestProfile{Model: modelA}); err != nil {
			t.Errorf("Pick() = %v, want served once the gate opens", err)
		}
	}
	parked.inFlight.Go(parkedPick)
	select {
	case <-h.session(t, escrowID).entered:
	case <-time.After(2 * time.Second):
		t.Fatalf("escrow %q never reached its session", escrowID)
	}
	parked.inFlight.Go(parkedPick)
	eventually(t, "the submit queue of "+escrowID+" to fill", func() bool { return h.queueDepth(escrowID) == 1 })
}

// Test flow:
//  1. Build a `fullQueueHarness` where `escrowA` outweighs `escrowB`, then fill `escrowA`'s submit queue.
//  2. Pick for a third request.
//  3. Assert the pick reaches `escrowB`'s session instead of answering `ErrEscrowBusy`.
//  4. Open the gate and assert the third request is served on `escrowB`.
func TestARequestAFullQueueRefusesIsOfferedToAnIdleEscrow(t *testing.T) {
	test, parked := fullQueueHarness(t, escrowA, escrowB)
	test.fillQueue(t, escrowA, parked)

	results := make(chan pickResult, 1)
	go func() {
		assignment, err := test.scheduler.Pick(context.Background(), RequestProfile{Model: modelA})
		results <- pickResult{assignment: assignment, err: err}
	}()

	select {
	case <-test.session(t, escrowB).entered:
	case early := <-results:
		t.Fatalf("Pick() = %v, want routed to the idle escrow %q", early.err, escrowB)
	case <-time.After(2 * time.Second):
		t.Fatal("the idle escrow never reached its session")
	}
	parked.open()
	served := <-results
	if served.err != nil || served.assignment.Escrow != escrowB {
		t.Fatalf("Pick() = (%q, %v), want served on %q", served.assignment.Escrow, served.err, escrowB)
	}
}

// Test flow:
//  1. Build a `fullQueueHarness`, then fill `escrowA`'s submit queue and, through the next two picks, `escrowB`'s.
//  2. Pick for a fifth request.
//  3. Assert the pick fails with `ErrEscrowBusy`.
//  4. Open the gate and let the four parked picks finish.
func TestOnlyAFleetOfFullQueuesAnswersBusy(t *testing.T) {
	test, parked := fullQueueHarness(t, escrowA, escrowB)
	test.fillQueue(t, escrowA, parked)
	test.fillQueue(t, escrowB, parked)

	_, err := test.scheduler.Pick(context.Background(), RequestProfile{Model: modelA})

	parked.open()
	if !errors.Is(err, ErrEscrowBusy) {
		t.Fatalf("Pick() = %v, want ErrEscrowBusy once every queue is full", err)
	}
}
