package scheduler

import (
	"slices"
	"testing"
)

// Test flow:
//  1. Register a live dispatcher for an escrow, beside the one it replaced for an earlier session of the same escrow.
//  2. Let the replaced dispatcher retire itself, as it does when it idles out.
//  3. Assert it retired without reporting the escrow retired and without removing the live dispatcher: the escrow still serves, and its series must survive.
func TestAReplacedDispatcherRetiresWithoutRetiringItsEscrow(t *testing.T) {
	observer := &recordingObserver{}
	live := newDispatcher(dispatcherDeps{escrowID: "escrow-a", sessionID: 2})
	replaced := newDispatcher(dispatcherDeps{escrowID: "escrow-a", sessionID: 1})
	scheduler := &Scheduler{dispatchers: map[string]*dispatcher{"escrow-a": live}, observer: observer}

	if !scheduler.retire(replaced) {
		t.Fatal("retire() = false, want the idle replaced dispatcher to retire")
	}

	if scheduler.dispatchers["escrow-a"] != live {
		t.Fatal("the live dispatcher was removed")
	}
	observer.mu.Lock()
	retired := slices.Clone(observer.retired)
	observer.mu.Unlock()
	if len(retired) != 0 {
		t.Fatalf("escrows reported retired %v, want none: the escrow is still served", retired)
	}
}

// Test flow:
//  1. Supersede a started dispatcher, as a new session of its escrow does.
//  2. Stop the scheduler.
//  3. Assert the superseded dispatcher was stopped too: every dispatcher the scheduler started has an owner that waits for it.
func TestStoppingTheSchedulerStopsASupersededDispatcher(t *testing.T) {
	superseded := newDispatcher(dispatcherDeps{escrowID: "escrow-a", sessionID: 1})
	superseded.start()
	scheduler := &Scheduler{dispatchers: map[string]*dispatcher{}}
	scheduler.supersede(superseded)

	scheduler.Stop()

	if !superseded.isStopped() {
		t.Fatal("the superseded dispatcher still runs after Stop")
	}
}
