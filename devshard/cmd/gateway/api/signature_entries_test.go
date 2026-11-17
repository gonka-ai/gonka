package api

import (
	"testing"

	"devshard/types"
	"devshard/user"
)

func slotsSigned(slots ...uint32) types.Bitmap128 {
	var signed types.Bitmap128
	for _, slot := range slots {
		signed.Set(slot)
	}
	return signed
}

// Test flow:
//  1. Build signed slot bitmaps for two nonces and matching signature-status entries with their weight and quorum.
//  2. Call signatureEntries with both.
//  3. Assert one entry per nonce, in order.
//  4. Assert each entry carries the status weight, quorum flag and total slots from its status entry.
func TestSignatureEntriesCarryTheWeightBesideTheSlots(t *testing.T) {
	signed := map[uint64]types.Bitmap128{
		1: slotsSigned(0, 1),
		2: slotsSigned(0),
	}
	status := []user.SignatureStatusEntry{
		{Nonce: 1, SigWeight: 11, Total: 16, HasQuorum: true},
		{Nonce: 2, SigWeight: 4, Total: 16},
	}

	entries := signatureEntries(signed, status)

	if len(entries) != 2 || entries[0].Nonce != 1 || entries[1].Nonce != 2 {
		t.Fatalf("entries = %+v, want one per nonce in order", entries)
	}
	if entries[0].SigWeight != 11 || !entries[0].HasQuorum || entries[0].TotalSlots != 16 {
		t.Errorf("nonce 1 = %+v, want the quorum it reached", entries[0])
	}
	if entries[1].SigWeight != 4 || entries[1].HasQuorum {
		t.Errorf("nonce 2 = %+v, want the weight that fell short", entries[1])
	}
}

// Test flow:
//  1. Call signatureEntries with a signed-slots map for one nonce and no status entries.
//  2. Assert the nonce still appears with both of its slots.
//  3. Assert it carries no weight and no quorum, since no status was reported for it.
func TestANonceWithoutAStatusEntryStillListsItsSlots(t *testing.T) {
	entries := signatureEntries(map[uint64]types.Bitmap128{7: slotsSigned(3, 5)}, nil)

	if len(entries) != 1 || len(entries[0].Slots) != 2 {
		t.Fatalf("entries = %+v, want nonce 7 with both slots", entries)
	}
	if entries[0].SigWeight != 0 || entries[0].HasQuorum {
		t.Errorf("nonce 7 = %+v, want no weight claimed for it", entries[0])
	}
}
