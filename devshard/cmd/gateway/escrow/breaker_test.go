package escrow

import (
	"sync"
	"testing"
)

const suppressionSafetyBound = 10

func countSuppressedCalls(b *createBreaker, model, role string) int {
	count := 0
	for count <= suppressionSafetyBound {
		if !b.gated(model, role) {
			return count
		}
		count++
	}
	return count
}

// Test flow:
//  1. Create a fresh createBreaker.
//  2. Assert gated returns false for a key that has never recorded a failure.
func TestCreateBreakerFreshKeyNotGated(t *testing.T) {
	b := newCreateBreaker()

	if b.gated("model-a", "temp") {
		t.Fatal("gated() = true for a fresh key, want false")
	}
}

// Test flow:
//  1. Record a number of failures on one key, varied across cases from 1 to 5.
//  2. Count how many subsequent calls stay gated via `countSuppressedCalls`.
//  3. Assert the suppressed count matches the expected escalation ladder (1, 2, 4, capping at 4).
func TestCreateBreakerEscalationLadder(t *testing.T) {
	tests := []struct {
		name           string
		failures       int
		wantSuppressed int
	}{
		{name: "1 failure suppresses 1 call", failures: 1, wantSuppressed: 1},
		{name: "2 failures suppress 2 calls", failures: 2, wantSuppressed: 2},
		{name: "3 failures suppress 4 calls", failures: 3, wantSuppressed: 4},
		{name: "4 failures still cap at 4", failures: 4, wantSuppressed: 4},
		{name: "5 failures stay capped at 4", failures: 5, wantSuppressed: 4},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			b := newCreateBreaker()
			for range tt.failures {
				b.recordFailure("model-a", "temp")
			}

			got := countSuppressedCalls(b, "model-a", "temp")
			if got != tt.wantSuppressed {
				t.Fatalf("suppressed %d calls after %d failures, want %d", got, tt.failures, tt.wantSuppressed)
			}
		})
	}
}

// Test flow:
//  1. Record two failures on (model-a, temp), leaving a cooldown of 2 suppressed calls on that key.
//  2. Assert gated is false for the same model with a different role, and for a different model with the same role.
//  3. Assert (model-a, temp) still has its own 2 suppressed calls, untouched by checking the other keys.
func TestCreateBreakerKeysAreIndependent(t *testing.T) {
	b := newCreateBreaker()
	b.recordFailure("model-a", "temp")
	b.recordFailure("model-a", "temp")

	if b.gated("model-a", "regular") {
		t.Fatal("gated(model-a, regular) = true, want a different role to stay ungated")
	}
	if b.gated("model-b", "temp") {
		t.Fatal("gated(model-b, temp) = true, want a different model to stay ungated")
	}

	got := countSuppressedCalls(b, "model-a", "temp")
	if got != 2 {
		t.Fatalf("suppressed %d calls for (model-a,temp), want 2 (the failures recorded above, untouched by the other keys)", got)
	}
}

// Test flow:
//  1. Record three failures on (model-a, temp), which would set the next cooldown to 4 if the ladder were not wiped.
//  2. Call reset on that key.
//  3. Assert gated is false immediately after reset.
//  4. Record one more failure and assert only 1 call is suppressed, proving the ladder restarted instead of resuming.
func TestCreateBreakerResetClearsFailuresNotJustCooldown(t *testing.T) {
	b := newCreateBreaker()
	b.recordFailure("model-a", "temp")
	b.recordFailure("model-a", "temp")
	b.recordFailure("model-a", "temp")

	b.reset("model-a", "temp")

	if b.gated("model-a", "temp") {
		t.Fatal("gated() = true immediately after reset, want false")
	}

	b.recordFailure("model-a", "temp")
	got := countSuppressedCalls(b, "model-a", "temp")
	if got != 1 {
		t.Fatalf("suppressed %d calls after reset + 1 failure, want 1 (ladder must restart, not resume)", got)
	}
}

// Test flow:
//  1. Start 8 goroutines, each cycling through recordFailure, gated, and reset on one of two model/role keys.
//  2. Wait for all goroutines to finish.
//  3. Assert nothing panics or races (the -race detector guards the actual assertion).
func TestCreateBreakerConcurrentAccess(t *testing.T) {
	b := newCreateBreaker()
	models := []string{"model-a", "model-b"}
	roles := []string{"temp", "regular"}

	var wg sync.WaitGroup
	for workerIndex := range 8 {
		wg.Add(1)
		go func(workerIndex int) {
			defer wg.Done()
			model := models[workerIndex%len(models)]
			role := roles[workerIndex%len(roles)]
			for i := range 200 {
				switch i % 3 {
				case 0:
					b.recordFailure(model, role)
				case 1:
					b.gated(model, role)
				case 2:
					b.reset(model, role)
				}
			}
		}(workerIndex)
	}
	wg.Wait()
}
