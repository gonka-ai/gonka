package escrow

import (
	"context"
	"errors"
	"slices"
	"testing"

	"devshard/cmd/gateway/store"
)

const settleRetriesWhileBusy = 3

// Test flow:
//  1. Hold a parked escrow whose requests and owed votes keep it busy, and settle it on several ticks in a row, each deferred.
//  2. Assert none of those ticks narrates the escrow parked again: it was parked once, and a line per deferred tick reads as a fresh park.
//  3. Settle a serving escrow and assert that one is narrated parked, once.
func TestADeferredSettleOfAParkedEscrowIsNotNarratedAsAnotherPark(t *testing.T) {
	parked := store.DevshardRecord{EscrowID: "21", PrivateKeyEnv: "MODEL_A_KEY", Model: "model-a", SettlementPending: true}
	manager, _ := busyManager(t, parked, &callLog{})
	narrator := &recordingLifecycleNarrator{}
	manager.narrator = narrator

	for range settleRetriesWhileBusy {
		if _, err := manager.settle(context.Background(), parked, false); !errors.Is(err, ErrDevshardBusy) {
			t.Fatalf("settle() = %v, want ErrDevshardBusy", err)
		}
	}
	if parks := slices.Index(narrator.recorded(), "parked 21"); parks >= 0 {
		t.Fatalf("narration = %v, want no park line for an escrow already parked", narrator.recorded())
	}

	serving := store.DevshardRecord{EscrowID: "22", PrivateKeyEnv: "MODEL_A_KEY", Model: "model-a", Active: true}
	servingManager, _ := busyManager(t, serving, &callLog{})
	servingNarrator := &recordingLifecycleNarrator{}
	servingManager.narrator = servingNarrator
	_, _ = servingManager.settle(context.Background(), serving, false)
	if got := servingNarrator.recorded(); !slices.Equal(got, []string{"parked 22"}) {
		t.Fatalf("narration = %v, want the serving escrow narrated parked once", got)
	}
}
