package scheduler

import (
	"testing"
	"time"

	"devshard/cmd/gateway/chain"
)

// forecastGates builds an `availability` whose window rung blocks the given participants.
func forecastGates(blocked ...string) availability {
	refused := asSet(blocked)
	gates := openAvailability()
	gates.congested = windowFullWhen(func(participant string) bool { return refused[participant] })
	return gates
}

func forecastEscrow(latestNonce uint64, slots ...string) Escrow {
	return Escrow{ID: "escrow-forecast", Model: modelA, Session: &fakeSession{latestNonce: latestNonce, slots: slots}}
}

// Test flow:
//  1. For each table case of a latest nonce, blocked hosts, excluded hosts and nonces already queued ahead, build a `forecastEscrow` and `forecastGates`.
//  2. Call `expectedBurns` for a waiter with the case's exclusions.
//  3. Assert the result matches the case's expected burn count: the distance from the cursor to the first usable host.
func TestTheForecastCountsTheNoncesBetweenTheCursorAndAUsableHost(t *testing.T) {
	t.Parallel()

	group := []string{"host-0", "host-1", "host-2", "host-3"}
	testCases := []struct {
		name        string
		latestNonce uint64
		blocked     []string
		excluded    []string
		queuedAhead uint64
		want        int
	}{
		{
			name:        "the next nonce already names a host that can take it",
			latestNonce: 3,
			want:        0,
		},
		{
			name:        "one blocked host costs the one nonce bound to it",
			latestNonce: 3,
			blocked:     []string{"host-0"},
			want:        1,
		},
		{
			name:        "the walk crosses every blocked host in turn",
			latestNonce: 3,
			blocked:     []string{"host-0", "host-1", "host-2"},
			want:        3,
		},
		{
			name:        "a group nothing can serve is priced at one lap, not at infinity",
			latestNonce: 3,
			blocked:     group,
			want:        4,
		},
		{
			name:        "the walk wraps past the end of the group",
			latestNonce: 2,
			blocked:     []string{"host-3", "host-0"},
			want:        2,
		},
		{
			name:        "waiters already queued spend the nonces ahead of ours",
			latestNonce: 3,
			queuedAhead: 2,
			blocked:     []string{"host-2"},
			want:        1,
		},
		{
			name:        "a host this request excluded is no landing place either",
			latestNonce: 3,
			excluded:    []string{"host-0"},
			want:        1,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			queued := newWaiter(RequestProfile{Model: modelA, Exclude: testCase.excluded}, time.Time{})

			got := expectedBurns(forecastEscrow(testCase.latestNonce, group...), forecastGates(testCase.blocked...), queued, testCase.queuedAhead)

			if got != testCase.want {
				t.Fatalf("expectedBurns = %v, want %v", got, testCase.want)
			}
		})
	}
}

// Test flow:
//  1. Call `expectedBurns` on an escrow with no session at all.
//  2. Assert the result is 0.
//  3. Call `expectedBurns` on an escrow whose group is empty.
//  4. Assert the result is 0, priced neutrally rather than on a guess.
func TestTheForecastIsNeutralWithoutAGroupToWalk(t *testing.T) {
	t.Parallel()
	queued := newWaiter(RequestProfile{Model: modelA}, time.Time{})

	if got := expectedBurns(Escrow{ID: "no-session"}, forecastGates(), queued, 0); got != 0 {
		t.Fatalf("expectedBurns without a session = %v, want 0", got)
	}
	if got := expectedBurns(forecastEscrow(3), forecastGates(), queued, 0); got != 0 {
		t.Fatalf("expectedBurns over an empty group = %v, want 0", got)
	}
}

// Test flow:
//  1. Build a scheduler with two equally weighted, equally loaded candidates: "far" and "near".
//  2. Block the first three hosts of "far"'s group, so its next usable host is farther from the cursor.
//  3. Pick an escrow.
//  4. Assert "near" is picked, the escrow whose next nonce lands on a usable host.
func TestThePickPrefersTheEscrowThatServesOnItsNextNonce(t *testing.T) {
	t.Parallel()
	scheduler, _, _ := newScheduler(
		candidate{id: "far", activeUsers: 1, weight: 10, latestNonce: 3, slots: []string{"far-0", "far-1", "far-2", "far-3"}},
		candidate{id: "near", activeUsers: 1, weight: 10, latestNonce: 3, slots: []string{"near-0", "near-1", "near-2", "near-3"}},
	)
	limiter := scheduler.limiter.(*fakeLimiter)
	for _, blocked := range []string{"far-0", "far-1", "far-2"} {
		limiter.block(blocked)
	}

	picked, err := pickWith(scheduler, RequestProfile{Model: modelA}, chain.PhaseSnapshot{})

	if err != nil {
		t.Fatalf("pickEscrow: %v", err)
	}
	if picked.ID != "near" {
		t.Fatalf("picked %q, want the escrow whose next nonce lands on a usable host", picked.ID)
	}
}

// Test flow:
//  1. Build a scheduler with one candidate whose whole two-host group is blocked.
//  2. Pick an escrow.
//  3. Assert the pick succeeds and returns the busy escrow, so the drain's sweep can answer for it.
func TestAnEscrowWhoseWholeGroupIsBusyIsStillPicked(t *testing.T) {
	t.Parallel()
	scheduler, _, _ := newScheduler(
		candidate{id: "only", activeUsers: 0, weight: 10, latestNonce: 3, slots: []string{"only-0", "only-1"}},
	)
	limiter := scheduler.limiter.(*fakeLimiter)
	limiter.block("only-0")
	limiter.block("only-1")

	picked, err := pickWith(scheduler, RequestProfile{Model: modelA}, chain.PhaseSnapshot{})

	if err != nil {
		t.Fatalf("pickEscrow = %v, want the busy escrow so the drain can answer for it", err)
	}
	if picked.ID != "only" {
		t.Fatalf("picked %q, want only", picked.ID)
	}
}

// Test flow:
//  1. Build a scheduler with a "burning" candidate whose group forces two burns to reach a usable host, and a "loaded" candidate carrying the table case's active-user count.
//  2. Pick an escrow.
//  3. Assert the picked escrow matches the case: burning is chosen when two burns cost less than three in-flight requests, loaded when they cost more than one.
func TestABurnCostsTheSameAsARequestAlreadyInFlight(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name              string
		loadedActiveUsers int
		want              string
	}{
		{name: "two burns are cheaper than three requests in flight", loadedActiveUsers: 3, want: "burning"},
		{name: "two burns are dearer than one request in flight", loadedActiveUsers: 1, want: "loaded"},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			scheduler, _, _ := newScheduler(
				candidate{id: "burning", activeUsers: 0, weight: 10, latestNonce: 3, slots: []string{"burning-0", "burning-1", "burning-2", "burning-3"}},
				candidate{id: "loaded", activeUsers: testCase.loadedActiveUsers, weight: 10, latestNonce: 3, slots: []string{"loaded-0", "loaded-1"}},
			)
			limiter := scheduler.limiter.(*fakeLimiter)
			limiter.block("burning-0")
			limiter.block("burning-1")

			picked, err := pickWith(scheduler, RequestProfile{Model: modelA}, chain.PhaseSnapshot{})

			if err != nil {
				t.Fatalf("pickEscrow: %v", err)
			}
			if picked.ID != testCase.want {
				t.Fatalf("picked %q, want %q", picked.ID, testCase.want)
			}
		})
	}
}

// Test flow:
//  1. Build a scheduler with two candidates, "far" and "near".
//  2. For each table case (a full congestion window, a cut-off host, an ejected host, a host this escrow blocked for diverging), block the first three hosts of "far"'s group that way.
//  3. Pick an escrow.
//  4. Assert "near" is picked in every case, since the forecast steps over every rung the drain reads.
func TestTheForecastStepsOverEveryRungTheDrainReads(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name  string
		block func(scheduler *Scheduler, participant string)
	}{
		{
			name:  "a full congestion window",
			block: func(scheduler *Scheduler, participant string) { scheduler.limiter.(*fakeLimiter).block(participant) },
		},
		{
			name: "a cut-off host",
			block: func(scheduler *Scheduler, participant string) {
				scheduler.limiter.(*fakeLimiter).cutOffHost(participant)
			},
		},
		{
			name:  "an ejected host",
			block: func(scheduler *Scheduler, participant string) { scheduler.perf.(*fakePerf).eject(participant) },
		},
		{
			name:  "a host this escrow blocked for diverging",
			block: func(scheduler *Scheduler, participant string) { scheduler.BlockHost("far", participant) },
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			scheduler, _, _ := newScheduler(
				candidate{id: "far", activeUsers: 1, weight: 10, latestNonce: 3, slots: []string{"far-0", "far-1", "far-2", "far-3"}},
				candidate{id: "near", activeUsers: 1, weight: 10, latestNonce: 3, slots: []string{"near-0", "near-1", "near-2", "near-3"}},
			)
			for _, blocked := range []string{"far-0", "far-1", "far-2"} {
				testCase.block(scheduler, blocked)
			}

			picked, err := pickWith(scheduler, RequestProfile{Model: modelA}, chain.PhaseSnapshot{})

			if err != nil {
				t.Fatalf("pickEscrow: %v", err)
			}
			if picked.ID != "near" {
				t.Fatalf("picked %q, want the escrow the forecast can reach", picked.ID)
			}
		})
	}
}

// Test flow:
//  1. Build a scheduler with two candidates sharing the same slot names, "blocked" and "clean".
//  2. Block both of "blocked"'s hosts for diverging.
//  3. Pick an escrow.
//  4. Assert "clean" is picked, so one escrow's divergence block does not cost another sharing the same host names.
func TestADivergenceBlockOnlyCostsTheEscrowThatCarriesIt(t *testing.T) {
	t.Parallel()
	scheduler, _, _ := newScheduler(
		candidate{id: "blocked", activeUsers: 0, weight: 10, latestNonce: 3, slots: []string{"shared-0", "shared-1"}},
		candidate{id: "clean", activeUsers: 0, weight: 10, latestNonce: 3, slots: []string{"shared-0", "shared-1"}},
	)
	scheduler.BlockHost("blocked", "shared-0")
	scheduler.BlockHost("blocked", "shared-1")

	picked, err := pickWith(scheduler, RequestProfile{Model: modelA}, chain.PhaseSnapshot{})

	if err != nil {
		t.Fatalf("pickEscrow: %v", err)
	}
	if picked.ID != "clean" {
		t.Fatalf("picked %q, want the escrow that did not block these hosts", picked.ID)
	}
}

// Test flow:
//  1. Build a scheduler with a "queued" candidate whose two middle hosts are blocked, and a "quiet" candidate carrying one active user.
//  2. Pick an escrow before any dispatcher queue exists and assert "queued" is picked, its cursor clear.
//  3. Register a dispatcher for the "queued" escrow with one pending submit.
//  4. Pick again and assert "quiet" is now picked, since the live dispatcher's queue depth now weighs against "queued".
func TestThePickReadsAQueuesDepthFromTheLiveDispatcher(t *testing.T) {
	t.Parallel()
	scheduler, escrows, _ := newScheduler(
		candidate{id: "queued", activeUsers: 0, weight: 10, latestNonce: 3, slots: []string{"queued-0", "queued-1", "queued-2", "queued-3"}},
		candidate{id: "quiet", activeUsers: 1, weight: 10, latestNonce: 3, slots: []string{"quiet-0", "quiet-1", "quiet-2", "quiet-3"}},
	)
	scheduler.limiter.(*fakeLimiter).block("queued-1")
	scheduler.limiter.(*fakeLimiter).block("queued-2")

	if picked, err := pickWith(scheduler, RequestProfile{Model: modelA}, chain.PhaseSnapshot{}); err != nil || picked.ID != "queued" {
		t.Fatalf("picked %q (%v) before any queue, want the idle escrow whose cursor is clear", picked.ID, err)
	}

	queuedEscrow := escrows.byModel[modelA][0]
	waiting := newDispatcher(dispatcherDeps{escrowID: queuedEscrow.ID, sessionID: queuedEscrow.SessionID})
	waiting.pendingSubmits.Store(1)
	scheduler.dispatchers[queuedEscrow.ID] = waiting

	picked, err := pickWith(scheduler, RequestProfile{Model: modelA}, chain.PhaseSnapshot{})

	if err != nil {
		t.Fatalf("pickEscrow: %v", err)
	}
	if picked.ID != "quiet" {
		t.Fatalf("picked %q, want the escrow whose cursor is not behind a queue", picked.ID)
	}
}

// Test flow:
//  1. Build a scheduler with a "backlogged" candidate that serves nobody yet and a "busy" candidate serving one request, every host of both open.
//  2. Register a dispatcher for "backlogged" with five waiters queued behind a slow session.
//  3. Pick an escrow and assert "busy" is picked: waiters queued on an escrow are load it already carries, so a slow escrow sheds new requests to its neighbour instead of filling its queue.
func TestQueuedWaitersCountAsLoadAnEscrowCarries(t *testing.T) {
	t.Parallel()
	scheduler, escrows, _ := newScheduler(
		candidate{id: "backlogged", activeUsers: 0, weight: 10, latestNonce: 3, slots: []string{"backlogged-0", "backlogged-1"}},
		candidate{id: "busy", activeUsers: 1, weight: 10, latestNonce: 3, slots: []string{"busy-0", "busy-1"}},
	)
	backlogged := escrows.byModel[modelA][0]
	slow := newDispatcher(dispatcherDeps{escrowID: backlogged.ID, sessionID: backlogged.SessionID})
	slow.pendingSubmits.Store(5)
	scheduler.dispatchers[backlogged.ID] = slow

	picked, err := pickWith(scheduler, RequestProfile{Model: modelA}, chain.PhaseSnapshot{})

	if err != nil {
		t.Fatalf("pickEscrow: %v", err)
	}
	if picked.ID != "busy" {
		t.Fatalf("picked %q, want the escrow with one request in flight over the one with five queued", picked.ID)
	}
}
