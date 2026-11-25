package settlement

import (
	"encoding/hex"
	"maps"
	"testing"

	"devshard/state"
	"devshard/types"
)

const fixedRootHex = "aee5b119136e3a10ae836bfadf25e3fdd6c5b5d4e59c5bc29bacf67ae0394d22"

func fixedPayload() state.SettlementPayload {
	return state.SettlementPayload{
		EscrowID:                    "77",
		StateRootAndProtocolVersion: "v7-fixed",
		Nonce:                       41,
		Fees:                        1001,
		RestHash:                    []byte("fixed-rest-hash-thirty-two-bytes"),
		HostStats: map[uint32]*types.HostStats{
			2: {Missed: 1, Invalid: 2, Cost: 300, RequiredValidations: 4, CompletedValidations: 3},
			0: {Missed: 0, Invalid: 0, Cost: 120, RequiredValidations: 2, CompletedValidations: 2},
			5: {Missed: 3, Invalid: 1, Cost: 75, RequiredValidations: 1, CompletedValidations: 0},
		},
		Signatures: map[uint32][]byte{0: []byte("sig-0")},
	}
}

// Test flow:
//  1. Build a fixed settlement payload with three non-contiguous slots, non-zero fees and a non-empty version.
//  2. Call `Root` on it.
//  3. Assert the root equals the stored bytes, computed once from the expressions devshardctl used before it called `Root`.
func TestRootMatchesTheFixedVector(t *testing.T) {
	root, err := Root(fixedPayload())
	if err != nil {
		t.Fatalf("Root = %v, want nil", err)
	}
	if got := hex.EncodeToString(root); got != fixedRootHex {
		t.Fatalf("root = %s, want %s", got, fixedRootHex)
	}
}

// Test flow:
//  1. Build a host-stats map with entries on slots 0 and 2 and a nil entry on slot 1.
//  2. Call `PresentStats` on it.
//  3. Assert the result holds slots 0 and 2 with the same entries, and the input still holds all three slots.
func TestPresentStatsDropsNilEntries(t *testing.T) {
	first := &types.HostStats{Cost: 10}
	third := &types.HostStats{Cost: 30}
	perSlot := map[uint32]*types.HostStats{0: first, 1: nil, 2: third}

	present := PresentStats(perSlot)

	want := map[uint32]*types.HostStats{0: first, 2: third}
	if !maps.Equal(present, want) {
		t.Fatalf("present stats = %v, want %v", present, want)
	}
	if len(perSlot) != 3 {
		t.Fatalf("input map has %d entries after PresentStats, want 3", len(perSlot))
	}
}
