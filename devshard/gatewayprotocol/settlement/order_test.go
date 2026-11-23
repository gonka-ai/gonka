package settlement

import (
	"reflect"
	"testing"

	"devshard/types"
)

// Test flow:
//  1. Build a host-stats map on slots 7, 0 and 3 with distinct counters.
//  2. Call `HostStatsBySlot` on it.
//  3. Assert the list is in ascending slot order and every counter is carried.
func TestHostStatsBySlotListsAscendingSlots(t *testing.T) {
	perSlot := map[uint32]*types.HostStats{
		7: {Missed: 1, Invalid: 2, Cost: 700, RequiredValidations: 3, CompletedValidations: 4, Validated: 13, Finished: 14},
		0: {Missed: 5, Invalid: 6, Cost: 100, RequiredValidations: 7, CompletedValidations: 8, Validated: 15, Finished: 16},
		3: {Missed: 9, Invalid: 10, Cost: 300, RequiredValidations: 11, CompletedValidations: 12, Validated: 17, Finished: 18},
	}

	stats := HostStatsBySlot(perSlot)

	want := []HostStat{
		{SlotID: 0, Missed: 5, Invalid: 6, Cost: 100, RequiredValidations: 7, CompletedValidations: 8, Validated: 15, Finished: 16},
		{SlotID: 3, Missed: 9, Invalid: 10, Cost: 300, RequiredValidations: 11, CompletedValidations: 12, Validated: 17, Finished: 18},
		{SlotID: 7, Missed: 1, Invalid: 2, Cost: 700, RequiredValidations: 3, CompletedValidations: 4, Validated: 13, Finished: 14},
	}
	if !reflect.DeepEqual(stats, want) {
		t.Fatalf("host stats = %+v, want %+v", stats, want)
	}
}

// Test flow:
//  1. Build a signature map on slots 4, 1 and 9.
//  2. Call `SignaturesBySlot` on it.
//  3. Assert the list is in ascending slot order and each signature stays with its slot.
func TestSignaturesBySlotListsAscendingSlots(t *testing.T) {
	perSlot := map[uint32][]byte{4: []byte("sig-4"), 1: []byte("sig-1"), 9: []byte("sig-9")}

	signatures := SignaturesBySlot(perSlot)

	want := []SlotSignature{
		{SlotID: 1, Signature: []byte("sig-1")},
		{SlotID: 4, Signature: []byte("sig-4")},
		{SlotID: 9, Signature: []byte("sig-9")},
	}
	if !reflect.DeepEqual(signatures, want) {
		t.Fatalf("signatures = %+v, want %+v", signatures, want)
	}
}
