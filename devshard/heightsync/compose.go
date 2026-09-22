package heightsync

import "devshard/types"

// FilterHostRaises drops host-signed stamps that would raise F without matching
// this hop's response-leg envelope (spec §14 rule 2 at honest compose).
//
// A carry (stamp ≤ floor) always stays. Sequencer-composed txs stay: they
// never raise F. When envelopePresent is false the hop omitted the section
// (or the caller has no envelope, as in in-process tests) and the check is
// skipped — Observe will still refuse a user-only raise, and L4 cannot run
// without a section.
//
// ownSlots is the responding host's slots. An ack signed by another slot is
// left alone: that hop's envelope is not this hop's, and dropping it would
// stall a turn on gossiped mempool copies.
func FilterHostRaises(floor, envelopeH uint64, envelopePresent bool, ownSlots map[uint32]struct{}, txs []*types.DevshardTx) (kept []*types.DevshardTx, omitted int) {
	if !envelopePresent || len(txs) == 0 {
		return txs, 0
	}
	kept = make([]*types.DevshardTx, 0, len(txs))
	for _, tx := range txs {
		if tx == nil {
			continue
		}
		if omitHostRaise(floor, envelopeH, ownSlots, tx) {
			omitted++
			continue
		}
		kept = append(kept, tx)
	}
	return kept, omitted
}

func omitHostRaise(floor, envelopeH uint64, ownSlots map[uint32]struct{}, tx *types.DevshardTx) bool {
	h, _, ok := RefStamp(tx)
	if !ok || h <= floor {
		return false
	}
	switch {
	case tx.GetHeightAck() != nil:
		slot := tx.GetHeightAck().SlotId
		if _, mine := ownSlots[slot]; !mine {
			return false
		}
		return h != envelopeH
	case tx.GetConfirmStart() != nil, tx.GetFinishInference() != nil:
		return h != envelopeH
	default:
		return false
	}
}
