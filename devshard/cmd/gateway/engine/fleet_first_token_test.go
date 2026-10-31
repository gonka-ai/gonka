package engine

import (
	"testing"
	"time"

	"devshard/cmd/gateway/scheduler"
)

var fleetSpread = [...]float64{0.8, 0.9, 1, 1.1, 1.2}

func feedFleet(fleet *fleetFirstToken, start time.Time, step time.Duration, count int, level float64) {
	for index := range count {
		attempt := &liveAttempt{}
		sent := start.Add(time.Duration(index) * step)
		wait := time.Duration(level * fleetSpread[index%len(fleetSpread)] * float64(firstTokenCurve(0)))
		fleet.recordDispatch(attempt, sent, 0)
		fleet.recordFirstToken(attempt, sent.Add(wait))
		fleet.forget(attempt)
	}
}

// Test flow:
//  1. Feed one first attempt short of the minimum sample count.
//  2. Assert the fleet scale is still unknown, so the hedge keeps the fixed curve.
func TestFleetScaleIsUnknownBelowTheMinimumSamples(t *testing.T) {
	fleet := newFleetFirstToken()
	feedFleet(fleet, raceStart, 100*time.Millisecond, fleetMinimumSamples-1, 0.2)

	scale := fleet.hedgeScale(raceStart.Add(10 * time.Second))

	if scale != 0 {
		t.Fatalf("hedgeScale = %v below %d samples, want 0", scale, fleetMinimumSamples)
	}
}

// Test flow:
//  1. Feed a fleet answering at a fifth of the curve, then, past the window, the same fleet five times slower.
//  2. Assert the scale sits under half the curve before the slowdown, and after it moves to just above the fleet's new level and no further, so the old samples are gone and a global slowdown hedges no more than a healthy fleet.
func TestFleetScaleFollowsAFleetWideSlowdown(t *testing.T) {
	fleet := newFleetFirstToken()
	feedFleet(fleet, raceStart, 100*time.Millisecond, 100, 0.2)
	before := fleet.hedgeScale(raceStart.Add(12 * time.Second))
	slowdown := raceStart.Add(fleetWindow + 20*time.Second)
	feedFleet(fleet, slowdown, 100*time.Millisecond, 100, 1)

	after := fleet.hedgeScale(slowdown.Add(12 * time.Second))

	if before <= 0 || before >= 0.5 {
		t.Fatalf("hedgeScale before the slowdown = %v, want within (0, 0.5)", before)
	}
	if after <= 1.2 || after >= 2 {
		t.Fatalf("hedgeScale after a fivefold slowdown = %v, want within (1.2, 2): above the fleet's new level, with the fast samples out of the window", after)
	}
}

// Test flow:
//  1. Feed a fleet at a fifth of the curve in which one host in five answers five times slower.
//  2. Assert the scale stays under half the curve, so that host's attempts still cross it and hedge.
func TestFleetScaleStillHedgesTheOneSlowHost(t *testing.T) {
	fleet := newFleetFirstToken()
	for index := range 100 {
		level := 0.2
		if index%5 == 0 {
			level = 1
		}
		feedFleet(fleet, raceStart.Add(time.Duration(index)*100*time.Millisecond), time.Millisecond, 1, level)
	}

	scale := fleet.hedgeScale(raceStart.Add(12 * time.Second))

	if scale <= 0 || scale >= 0.5 {
		t.Fatalf("hedgeScale with one slow host in five = %v, want within (0, 0.5)", scale)
	}
}

// Test flow:
//  1. Feed a fast fleet into four trackers, then dispatch sixty first attempts that are still waiting two seconds later into one, sixty that gave up without a token into another, and sixty dispatched only a moment ago, younger than the fleet's typical wait, into a third.
//  2. Assert the waiting attempts lift the scale at least twice over, while attempts that gave up and attempts younger than the typical wait leave it where the fourth tracker has it.
func TestFleetScaleCountsAttemptsStillWaiting(t *testing.T) {
	waiting, gaveUp, young, baseline := newFleetFirstToken(), newFleetFirstToken(), newFleetFirstToken(), newFleetFirstToken()
	for _, fleet := range []*fleetFirstToken{waiting, gaveUp, young, baseline} {
		feedFleet(fleet, raceStart, 100*time.Millisecond, 60, 0.2)
	}
	now := raceStart.Add(8 * time.Second)
	for range 60 {
		waiting.recordDispatch(&liveAttempt{}, now.Add(-2*time.Second), 0)
		failed := &liveAttempt{}
		gaveUp.recordDispatch(failed, now.Add(-2*time.Second), 0)
		gaveUp.forget(failed)
		young.recordDispatch(&liveAttempt{}, now.Add(-10*time.Millisecond), 0)
	}

	withWaiting, withGaveUp, withYoung, fast := waiting.hedgeScale(now), gaveUp.hedgeScale(now), young.hedgeScale(now), baseline.hedgeScale(now)

	if fast <= 0 {
		t.Fatalf("hedgeScale of the fast fleet = %v, want a known scale", fast)
	}
	if withWaiting < 2*fast {
		t.Fatalf("hedgeScale with attempts waiting = %v, want at least twice the fast fleet's %v", withWaiting, fast)
	}
	if withGaveUp != fast || withYoung != fast {
		t.Fatalf("hedgeScale after attempts gave up = %v and with young attempts = %v, want the fast fleet's %v", withGaveUp, withYoung, fast)
	}
}

// Test flow:
//  1. Build a fleet whose scale is known, read it, feed it a fivefold slowdown whose tokens already arrived, read it at an earlier clock, and then inside the refresh.
//  2. Assert both reads return the cached scale, so an earlier clock does not restart the refresh, and a read a full refresh later sees the slowdown.
func TestFleetScaleRefreshesAtMostOncePerInterval(t *testing.T) {
	fleet := newFleetFirstToken()
	feedFleet(fleet, raceStart, 10*time.Millisecond, 100, 0.2)
	measuredAt := raceStart.Add(5 * time.Second)
	cached := fleet.hedgeScale(measuredAt)
	feedFleet(fleet, measuredAt.Add(-2500*time.Millisecond), time.Millisecond, 400, 1)

	earlier := fleet.hedgeScale(measuredAt.Add(-time.Second))
	inside := fleet.hedgeScale(measuredAt.Add(fleetScaleRefresh / 2))
	refreshed := fleet.hedgeScale(measuredAt.Add(fleetScaleRefresh + time.Second))

	if inside != cached || earlier != cached {
		t.Fatalf("hedgeScale inside the refresh = %v and at an earlier clock = %v, want the cached %v", inside, earlier, cached)
	}
	if refreshed <= cached {
		t.Fatalf("hedgeScale a refresh later = %v, want above the cached %v after a fivefold slowdown", refreshed, cached)
	}
}

// Test flow:
//  1. Wire a fleet tracker into a race, dispatch its trusted first attempt and its second, give both a first token, and then dispatch the first attempt of a race that started two because its primary was distrusted.
//  2. Assert only the trusted first attempt became a sample, and the distrusted primary is not waiting in the tracker.
func TestARaceFeedsTheFleetWithItsTrustedFirstAttemptOnly(t *testing.T) {
	fleet := newFleetFirstToken()
	first, second := &liveAttempt{nonce: 1}, &liveAttempt{nonce: 2}
	trusted := &raceCoordinator{
		deps:     raceDeps{Fleet: fleet},
		attempts: []*liveAttempt{first, second},
		byNonce:  map[uint64]*liveAttempt{1: first, 2: second},
		decision: StartPrimary,
	}
	distrustedPrimary := &liveAttempt{nonce: 3}
	distrusted := &raceCoordinator{
		deps:     raceDeps{Fleet: fleet},
		attempts: []*liveAttempt{distrustedPrimary},
		byNonce:  map[uint64]*liveAttempt{3: distrustedPrimary},
		decision: StartPrimarySuspicious,
	}

	for _, nonce := range []uint64{1, 2} {
		trusted.apply(AttemptEvent{Kind: AttemptDispatched, Nonce: nonce, At: raceStart})
		trusted.apply(AttemptEvent{Kind: AttemptFirstToken, Nonce: nonce, At: raceStart.Add(time.Second)})
	}
	distrusted.apply(AttemptEvent{Kind: AttemptDispatched, Nonce: 3, At: raceStart})

	if fleet.filled != 1 {
		t.Fatalf("fleet samples = %d, want only the trusted first attempt's", fleet.filled)
	}
	if len(fleet.waiting) != 0 {
		t.Fatalf("fleet waiting attempts = %d, want none from a distrusted primary", len(fleet.waiting))
	}
}

// Test flow:
//  1. Dispatch a race's first attempt into its fleet tracker, then abandon it after a cancel, the way a host that ignores its context is given up on.
//  2. Assert the tracker no longer holds it as waiting.
func TestAnAbandonedAttemptLeavesTheFleet(t *testing.T) {
	fixture := newRaceFixture(settledPolicy(), 1)
	fleet := newFleetFirstToken()
	fixture.deps.Fleet = fleet
	primary := &liveAttempt{nonce: 700, participant: "host-0", launchTime: testEpoch, cancel: func() {}}
	coordinator := pausedCoordinator(fixture, 1, primary)
	coordinator.decision = StartPrimary
	coordinator.apply(AttemptEvent{Kind: AttemptDispatched, Nonce: 700, At: testEpoch})

	coordinator.abandonCancelled()

	if len(fleet.waiting) != 0 {
		t.Fatalf("fleet waiting attempts = %d after the attempt was abandoned, want 0", len(fleet.waiting))
	}
}

// Test flow:
//  1. Wire a fleet tracker whose scale is known into a race, launch an attempt on a scripted host that holds before it sends, and plan the race.
//  2. Assert the attempt took the tracker's scale at launch and the plan hands it to the policy.
func TestAnAttemptTakesTheFleetScaleAtLaunch(t *testing.T) {
	fixture := newRaceFixture(settledPolicy(), 1)
	fleet := newFleetFirstToken()
	feedFleet(fleet, testEpoch.Add(-10*time.Second), 100*time.Millisecond, 60, 0.2)
	fixture.deps.Fleet = fleet
	arrived, never := make(chan uint64, 1), make(chan struct{})
	fixture.host(800, 0, "host-0", &hostScript{arrive: arrived, release: never})
	coordinator := pausedCoordinator(fixture, 1)
	assignment, _ := fixture.picker.Pick(t.Context(), scheduler.RequestProfile{})
	want := fleet.hedgeScale(fixture.clock.Now())

	coordinator.launch(assignment, RolePrimary, StartPrimary)
	waitForValue(t, arrived, "the attempt reaching its host")
	plan := coordinator.plan()
	coordinator.attempts[0].cancel()

	if want <= 0 || coordinator.attempts[0].fleetScale != want || plan.Attempts[0].FleetHedgeScale != want {
		t.Fatalf("attempt fleet scale = %v, plan = %v, want the tracker's %v", coordinator.attempts[0].fleetScale, plan.Attempts[0].FleetHedgeScale, want)
	}
}
