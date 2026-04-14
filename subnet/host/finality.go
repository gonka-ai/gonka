package host

import (
	"subnet/logging"
	"subnet/storage"
	"subnet/types"
)

// computeFinalizedNonce returns the highest nonce F such that >=2/3 of the
// session group (by slot count) has signed at nonce >= F.
//
// A slot that signed at nonce n is considered to have implicitly confirmed all
// nonces <= n by building on top of them.
func computeFinalizedNonce(store storage.Storage, escrowID string, latestNonce uint64, group []types.SlotAssignment) uint64 {
	// Walk nonces high→low. After processing nonce n, running is the set of slots that
	// signed at any nonce in [n, latestNonce], i.e. signed at nonce >= n 
	threshold := twoThirdsWeight(group)
	var running types.Bitmap128
	for n := latestNonce; n > 0; n-- {
		sigs, err := store.GetSignatures(escrowID, n)
		if err != nil {
			logging.Error("get signatures failed", "subsystem", "host", "escrow_id", escrowID, "nonce", n, "error", err)
		} else {
			for slotID := range sigs {
				running.Set(slotID)
			}
		}
		if bitmapSlotWeight(running, group) >= threshold {
			return n
		}
	}
	return 0 // warm-up period: not yet finalized
}

// bitmapSlotWeight counts the number of group slots whose bit is set in bm.
func bitmapSlotWeight(bm types.Bitmap128, group []types.SlotAssignment) uint32 {
	var total uint32
	for _, sa := range group {
		if bm.IsSet(sa.SlotID) {
			total++
		}
	}
	return total
}

// twoThirdsWeight returns ceil(2/3 * totalSlots).
func twoThirdsWeight(group []types.SlotAssignment) uint32 {
	total := uint32(len(group))
	return (total*2 + 2) / 3
}
