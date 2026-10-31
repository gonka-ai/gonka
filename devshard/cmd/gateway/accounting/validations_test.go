package accounting

import (
	"testing"

	"devshard/types"
)

// Test flow:
//  1. Open an escrow with two slots and observe host stats on slot 1 reporting required and completed validation counts.
//  2. Find slot 1's record.
//  3. Assert its required and completed validation counts match what was observed.
func TestValidationCountsReachTheRecordFromHostStats(t *testing.T) {
	book := NewBook(nil)
	if err := book.OpenEscrow(EscrowMetadata{
		EscrowID:      "e1",
		CreationEpoch: 9,
		Model:         "m",
		Slots:         []types.SlotAssignment{{SlotID: 0, ValidatorAddress: "p0"}, {SlotID: 1, ValidatorAddress: "p1"}},
	}); err != nil {
		t.Fatalf("OpenEscrow: %v", err)
	}
	if err := book.ObserveHostStats("e1", 1, types.HostStats{RequiredValidations: 7, CompletedValidations: 4}); err != nil {
		t.Fatalf("ObserveHostStats: %v", err)
	}

	for _, record := range book.Query(QueryFilter{EpochIndex: 9}) {
		if record.Participant != "p1" {
			continue
		}
		if record.RequiredValidations != 7 || record.CompletedValidations != 4 {
			t.Fatalf("validations = %d/%d, want 7/4", record.CompletedValidations, record.RequiredValidations)
		}
		return
	}
	t.Fatal("no record for the slot that carries the stats")
}
