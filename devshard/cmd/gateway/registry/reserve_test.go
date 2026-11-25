package registry

import (
	"context"
	"reflect"
	"testing"
)

// Test flow:
//  1. Add one escrow with AddReserve and one with Add, for the same model, with a recording exhaustion sink.
//  2. Assert Candidates marks only the first as a reserve.
//  3. Call ReserveTaken on the reserve; assert Candidates now shows it as a regular, and the sink heard OnReserveTaken once.
//  4. Call ReserveTaken again, and once for the regular; assert the sink is not told again.
func TestTakingAReserveTurnsItIntoARegularEscrowOnce(t *testing.T) {
	t.Parallel()
	rotation := &recordingExhaustion{}
	registry := New(Deps{
		ServingSessions: newSessions(map[string]*fakeSession{"1": newFakeSession("hostA"), "2": newFakeSession("hostB")}).open,
		Exhaustion:      rotation,
		Now:             fixedClock(),
	})
	if err := registry.AddReserve(context.Background(), "1", "qwen"); err != nil {
		t.Fatalf("AddReserve = %v, want nil", err)
	}
	mustAdd(t, registry, "2", "qwen")

	if got, want := reservesOf(registry), map[string]bool{"1": true, "2": false}; !reflect.DeepEqual(got, want) {
		t.Fatalf("candidate reserve flags = %v, want %v", got, want)
	}

	registry.ReserveTaken("1")
	registry.ReserveTaken("1")
	registry.ReserveTaken("2")

	if got, want := reservesOf(registry), map[string]bool{"1": false, "2": false}; !reflect.DeepEqual(got, want) {
		t.Fatalf("candidate reserve flags after the take = %v, want %v", got, want)
	}
	if got, want := rotation.reservesTaken(), []string{"1"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("OnReserveTaken calls = %v, want %v", got, want)
	}
}

func reservesOf(registry *Registry) map[string]bool {
	flags := map[string]bool{}
	for _, candidate := range registry.Candidates("qwen") {
		flags[candidate.ID] = candidate.IsReserve
	}
	return flags
}
