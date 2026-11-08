package engine

import (
	"bytes"
	"strconv"
	"sync"
	"testing"

	"devshard/cmd/gateway/config"
)

func testBudget(attempt, participant, global int64) *carryBudget {
	return newCarryBudget(config.Stream{
		ClassifyMaxAttemptBytes:     attempt,
		ClassifyMaxParticipantBytes: participant,
		ClassifyMaxGlobalBytes:      global,
	})
}

func assertUsage(t *testing.T, budget *carryBudget, participant string, wantParticipant, wantGlobal int64) {
	t.Helper()
	participantBytes, globalBytes := budget.counterFor(participant).Load(), budget.global.Load()
	if participantBytes != wantParticipant || globalBytes != wantGlobal {
		t.Fatalf("usage(%q) = (%d, %d), want (%d, %d)", participant, participantBytes, globalBytes, wantParticipant, wantGlobal)
	}
}

// Test flow:
//  1. Build a budget with a tiny global cap (32) via `testBudget`.
//  2. Reserve 24 bytes for "alice" and assert it succeeds with usage recorded.
//  3. Reserve 16 more bytes, past the global cap, and assert it is rejected with usage unchanged.
func TestCarryBudgetGlobalTripRollsBackParticipantCharge(t *testing.T) {
	budget := testBudget(1<<20, 1<<20, 32)
	counter := budget.counterFor("alice")

	if budget.reserve(counter, 24) != budgetGranted {
		t.Fatal("first reserve rejected")
	}
	assertUsage(t, budget, "alice", 24, 24)

	if budget.reserve(counter, 16) != budgetGlobalFull {
		t.Fatal("reserve past the global cap was not refused for the global pool")
	}
	assertUsage(t, budget, "alice", 24, 24)
}

// Test flow:
//  1. Build a budget with a tiny participant cap (32).
//  2. Reserve 24 bytes for "alice" (succeeds), then 16 more (rejected by the participant cap); assert alice's usage stays unchanged.
//  3. Reserve 24 bytes for "bob", a different participant, and assert it succeeds and adds to the global total.
func TestCarryBudgetParticipantTripLeavesGlobalUntouched(t *testing.T) {
	budget := testBudget(1<<20, 32, 1<<20)
	alice, bob := budget.counterFor("alice"), budget.counterFor("bob")

	if budget.reserve(alice, 24) != budgetGranted {
		t.Fatal("first reserve rejected")
	}
	if budget.reserve(alice, 16) != budgetParticipantFull {
		t.Fatal("reserve past the participant cap was not refused for the participant")
	}
	assertUsage(t, budget, "alice", 24, 24)

	if budget.reserve(bob, 24) != budgetGranted {
		t.Fatal("a second participant was starved by the first's cap")
	}
	assertUsage(t, budget, "bob", 24, 48)
}

// Test flow:
//  1. Build a `carryBuffer` and feed it a chunk head with no terminator; assert `Take` returns nothing parseable yet and reports no drop.
//  2. Feed the tail that completes the event; assert `Take` returns the reassembled event and the buffer keeps the leftover tail.
//  3. Release the buffer twice and assert usage drops to zero and stays there.
func TestCarryBufferReassemblesAcrossChunks(t *testing.T) {
	budget := testBudget(1<<20, 1<<20, 1<<20)
	buffer := newCarryBuffer(budget, "alice")

	if parseable, dropped := buffer.Take([]byte(`data: {"x":1}`)); parseable != nil || dropped {
		t.Fatalf("Take(head) = (%q, %v), want (nil, false)", parseable, dropped)
	}
	assertUsage(t, budget, "alice", 13, 13)

	parseable, dropped := buffer.Take([]byte("\n\ndata: par"))
	if dropped {
		t.Fatal("Take(tail) reported a drop")
	}
	if want := "data: {\"x\":1}\n\n"; string(parseable) != want {
		t.Fatalf("parseable = %q, want %q", parseable, want)
	}
	if want := "data: par"; string(buffer.Tail()) != want {
		t.Fatalf("Tail() = %q, want %q", buffer.Tail(), want)
	}
	assertUsage(t, budget, "alice", 9, 9)

	buffer.Release()
	assertUsage(t, budget, "alice", 0, 0)
	buffer.Release()
	assertUsage(t, budget, "alice", 0, 0)
}

// Test flow:
//  1. Build a `carryBuffer` against a budget with an attempt cap smaller than one complete event.
//  2. Take the whole event in a single call.
//  3. Assert it is returned whole, not dropped, and never charged against the budget.
func TestCarryBufferNeverChargesACompleteEventLargerThanTheAttemptBudget(t *testing.T) {
	budget := testBudget(8, 1<<20, 1<<20)
	buffer := newCarryBuffer(budget, "alice")
	event := []byte("data: {\"x\":1}\n\n")

	parseable, dropped := buffer.Take(event)

	if !bytes.Equal(parseable, event) || dropped {
		t.Fatalf("Take() = %q, %v; want the whole event and no drop", parseable, dropped)
	}
	assertUsage(t, budget, "alice", 0, 0)
}

// Test flow:
//  1. For each of the attempt, participant and global caps in turn (the table varies which one is tiny), build a `carryBuffer` against a budget where only that cap is small.
//  2. Take an oversized chunk; assert the cap trip is reported, the raw chunk is still returned, and usage stays at zero.
//  3. Take the same oversized chunk again and assert the trip is not reported a second time for the same attempt.
//  4. Take a normal chunk afterward and assert reassembly resumes.
func TestCarryBufferCapsTripIndependently(t *testing.T) {
	oversized := bytes.Repeat([]byte("x"), 24)
	tests := []struct {
		name   string
		budget *carryBudget
	}{
		{name: "attempt", budget: testBudget(8, 1<<20, 1<<20)},
		{name: "participant", budget: testBudget(1<<20, 8, 1<<20)},
		{name: "global", budget: testBudget(1<<20, 1<<20, 8)},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			buffer := newCarryBuffer(testCase.budget, "alice")

			parseable, dropped := buffer.Take(oversized)
			if !dropped {
				t.Fatal("cap trip was not reported")
			}
			if !bytes.Equal(parseable, oversized) {
				t.Fatalf("parseable = %q, want the raw chunk", parseable)
			}
			assertUsage(t, testCase.budget, "alice", 0, 0)

			if _, dropped := buffer.Take(oversized); dropped {
				t.Fatal("a second cap trip was reported for the same attempt")
			}

			parseable, dropped = buffer.Take([]byte("ok\n"))
			if dropped {
				t.Fatal("reassembly did not resume after the trip")
			}
			if string(parseable) != "ok\n" {
				t.Fatalf("parseable = %q, want %q", parseable, "ok\n")
			}
		})
	}
}

// Test flow:
//  1. Take a head with no terminator; assert no drop and the full head charged as usage.
//  2. Take a short tail that replaces the retained head; assert no drop and usage shrinking to the new, smaller retained size.
func TestCarryBufferRetainedTailShrinkRefunds(t *testing.T) {
	budget := testBudget(1<<20, 1<<20, 1<<20)
	buffer := newCarryBuffer(budget, "alice")

	if _, dropped := buffer.Take([]byte("0123456789")); dropped {
		t.Fatal("Take(head) reported a drop")
	}
	assertUsage(t, budget, "alice", 10, 10)

	if _, dropped := buffer.Take([]byte("\nab")); dropped {
		t.Fatal("Take(tail) reported a drop")
	}
	assertUsage(t, budget, "alice", 2, 2)
}

// Test flow:
//  1. Start 16 concurrent workers, each running many rounds of `Take` against one of 4 shared participant buffers, then releasing.
//  2. Wait for all workers to finish.
//  3. Assert every participant's usage settles back to zero once all its buffers are released.
func TestCarryBudgetConcurrentAttemptsSettleToZero(t *testing.T) {
	budget := testBudget(1<<10, 1<<12, 1<<14)
	var attempts sync.WaitGroup
	for worker := range 16 {
		attempts.Go(func() {
			buffer := newCarryBuffer(budget, "participant-"+strconv.Itoa(worker%4))
			defer buffer.Release()
			for round := range 64 {
				buffer.Take(bytes.Repeat([]byte("x"), round))
				buffer.Take([]byte("payload\n\ntail"))
			}
		})
	}
	attempts.Wait()

	for participant := range 4 {
		assertUsage(t, budget, "participant-"+strconv.Itoa(participant), 0, 0)
	}
}
