package heightsync

import "devshard/types"

// VectorContradiction is an ACKED vector entry that the log does not contain.
// The receiver records this as a user-attributable mark (spec §14); it never
// INVALID-ates the diff.
type VectorContradiction struct {
	Slot         uint32
	ClaimedNonce uint64
	ClaimedH     uint64
}

// ComposeSyncVector reports turn s−1: one entry per slot from what the user held.
func ComposeSyncVector(slotsNum uint32, prev *SyncTurnRecord) []*types.SyncVectorEntry {
	if slotsNum == 0 {
		slotsNum = 1
	}
	out := make([]*types.SyncVectorEntry, 0, slotsNum)
	for slot := uint32(0); slot < slotsNum; slot++ {
		ent := &types.SyncVectorEntry{SlotId: slot, Status: types.AckStatus_MISSING}
		if prev != nil {
			if ack, ok := prev.Acks[slot]; ok {
				ent.Status = types.AckStatus_ACKED
				ent.ObservedHeight = ack.Height
				ent.AckNonce = ack.Nonce
			}
		}
		out = append(out, ent)
	}
	return out
}

// CheckVectorAgainstLog implements L7 honesty: ACKED must name an ack actually
// in the log. MISSING / UNREACHABLE / REJECTED with no ack is not blame.
func CheckVectorAgainstLog(vec []*types.SyncVectorEntry, logAcks map[uint32]AckRecord) []VectorContradiction {
	var out []VectorContradiction
	for _, ent := range vec {
		if ent == nil || ent.Status != types.AckStatus_ACKED {
			continue
		}
		ack, ok := logAcks[ent.SlotId]
		if !ok || ack.Nonce != ent.AckNonce {
			out = append(out, VectorContradiction{
				Slot:         ent.SlotId,
				ClaimedNonce: ent.AckNonce,
				ClaimedH:     ent.ObservedHeight,
			})
		}
	}
	return out
}
