package heightsync

import "devshard/types"

// StampPresent reports whether a Diff-resident (observed_height, observed_block_hash)
// pair is a real claim. Proto3 uint64 gives 0 for both "no stamp" and a literal
// zero, so presence is keyed on a non-empty hash (plan §8.5.1 residual / H38).
//
// L0 / L0b must skip any leg for which this is false; treating absence as
// height 0 would make a present-then-absent start/confirm pair look like a
// regression and INVALID every verifier.
func StampPresent(observedBlockHash []byte) bool {
	return len(observedBlockHash) > 0
}

// TxStamp returns the Diff-resident observed height on tx when the hash is present.
func TxStamp(tx *types.DevshardTx) (uint64, bool) {
	if tx == nil {
		return 0, false
	}
	if start := tx.GetStartInference(); start != nil {
		return inferenceStamp(start)
	}
	if conf := tx.GetConfirmStart(); conf != nil {
		return inferenceStamp(conf)
	}
	if fin := tx.GetFinishInference(); fin != nil {
		return inferenceStamp(fin)
	}
	if hb := tx.GetHeartbeat(); hb != nil {
		if StampPresent(hb.ObservedBlockHash) {
			return hb.ObservedHeight, true
		}
		return 0, false
	}
	if ack := tx.GetHeightAck(); ack != nil {
		if StampPresent(ack.ObservedBlockHash) {
			return ack.ObservedHeight, true
		}
	}
	return 0, false
}
