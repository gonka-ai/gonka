package escrow

import (
	"testing"
	"time"
)

// Test flow:
//  1. Read an escrow idle at two ticks ten minutes apart, with a race having taken it in between.
//  2. Read it idle once more, ten minutes later, with nothing having taken it.
//  3. Assert the race reset the idle clock, so only the untouched stretch counts toward retiring it.
func TestARaceBetweenTicksKeepsAnEscrowFromLookingIdle(t *testing.T) {
	planner := &fundingPlanner{models: map[string]*modelFunding{}, escrows: map[string]*escrowFunding{}}
	start := time.Unix(1_000, 0)

	planner.idleFor("escrow-1", true, 1, start)
	afterARace := planner.idleFor("escrow-1", true, 2, start.Add(10*time.Minute))
	untouched := planner.idleFor("escrow-1", true, 2, start.Add(20*time.Minute))

	if afterARace != 0 {
		t.Fatalf("idleFor(after a race) = %v, want 0: a race took the escrow since the last tick", afterARace)
	}
	if untouched != 10*time.Minute {
		t.Fatalf("idleFor(untouched) = %v, want 10m: counted from the tick that saw the race", untouched)
	}
}
