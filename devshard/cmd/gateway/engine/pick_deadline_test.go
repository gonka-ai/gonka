package engine

import (
	"testing"
	"time"
)

// Test flow:
//  1. Take a coordinator whose pick has started and whose pick deadline has fired, so the pick is cancelled but the scheduler has not answered yet.
//  2. Read the pick deadline the timer would be armed with next.
//  3. Assert there is none: a deadline already spent must not re-arm the timer at a moment in the past, which fires it again at once until the scheduler answers.
func TestAStoppedPickArmsNoFurtherDeadline(t *testing.T) {
	coordinator := &raceCoordinator{}
	coordinator.pickCancel, coordinator.pickStarted = func() {}, time.Unix(1_700_000_000, 0)

	coordinator.stopPicking()

	if deadline := coordinator.pickDeadline(); !deadline.IsZero() {
		t.Fatalf("pickDeadline() = %v after the pick was stopped, want none", deadline)
	}
}
