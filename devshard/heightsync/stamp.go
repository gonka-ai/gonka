package heightsync

import "devshard/types"

// StampPresent reports whether a Diff-resident (observed_height, observed_block_hash)
// pair is a real claim. Proto3 uint64 gives 0 for both "no stamp" and a literal
// zero, so presence is keyed on a non-empty hash (spec §14).
//
// L0 / L0b must skip any leg for which this is false; treating absence as
// height 0 would make a present-then-absent start/confirm pair look like a
// regression and INVALID every verifier.
func StampPresent(observedBlockHash []byte) bool {
	return len(observedBlockHash) > 0
}

// RefStamp returns the *reference height* stamped on tx: the height a producer
// wrote to timestamp a nonce.
//
// Every Diff-resident height is one of these — the inference legs, MsgHeartbeat
// and MsgHeightAck alike. There is deliberately no second semantics in the log:
// a producer stamps max(own_tip, F(m)) or omits, so a height in Diff answers
// "what is the honest logical time of this nonce", never "what does this
// follower see". First-party readings live in the HeightSyncSection at the edge
// (spec §7 / §8); see spec §14, *One height in the log*.
func RefStamp(tx *types.DevshardTx) (uint64, []byte, bool) {
	if tx == nil {
		return 0, nil, false
	}
	type stamped interface {
		GetObservedHeight() uint64
		GetObservedBlockHash() []byte
	}
	var msg stamped
	switch {
	case tx.GetStartInference() != nil:
		msg = tx.GetStartInference()
	case tx.GetConfirmStart() != nil:
		msg = tx.GetConfirmStart()
	case tx.GetFinishInference() != nil:
		msg = tx.GetFinishInference()
	case tx.GetHeartbeat() != nil:
		msg = tx.GetHeartbeat()
	case tx.GetHeightAck() != nil:
		msg = tx.GetHeightAck()
	default:
		return 0, nil, false
	}
	if !StampPresent(msg.GetObservedBlockHash()) {
		return 0, nil, false
	}
	return msg.GetObservedHeight(), msg.GetObservedBlockHash(), true
}

// ExecutorStamp returns the inference id and reference height of a host-signed
// executor stamp: MsgConfirmStart, whose executor_sig covers
// ExecutorReceiptContent, or MsgFinishInference, whose proposer_sig covers the
// message. Both bind the height to the executor slot, so a stamped leg proves
// the same host round-trip a MsgHeightAck does and is worth the same claim on
// the cadence (spec §10.5). The id attributes it: the executor of inference i is
// group[i % len(group)], the arithmetic RefProducingNonce already relies on.
//
// MsgStartInference is excluded even though it carries a stamp: that height is
// the sequencer's own reading, and a producer cannot discharge its own cadence.
func ExecutorStamp(tx *types.DevshardTx) (inferenceID, height uint64, ok bool) {
	if tx == nil {
		return 0, 0, false
	}
	var id uint64
	var msg any
	switch {
	case tx.GetConfirmStart() != nil:
		id, msg = tx.GetConfirmStart().InferenceId, tx.GetConfirmStart()
	case tx.GetFinishInference() != nil:
		id, msg = tx.GetFinishInference().InferenceId, tx.GetFinishInference()
	default:
		return 0, 0, false
	}
	h, stamped := inferenceStamp(msg)
	if !stamped {
		return 0, 0, false
	}
	return id, h, true
}

// RefProducingNonce is the nonce whose handling produced a reference stamp.
//
// This is the nonce the stamp must be judged against, not the nonce it lands at:
// a leg is queued into a later diff, so comparing it against the floor where it
// lands asks it to have known a height that did not exist when it was made,
// which pipelining guarantees will happen. No extra wire field is needed to
// recover it for any message type:
//
// In every case the answer is "one past the highest nonce the producer had
// certainly applied", which each message type already names:
//
//   - confirm / finish carry inference_id, which *is* the nonce assigned at
//     PrepareInference;
//   - start and heartbeat are sequencer-composed, so they are produced at the
//     nonce they land at;
//   - an ack answers the heartbeat at ref_nonce, so its producer had applied
//     through ref_nonce inclusive — the basis is ref_nonce + 1, which folds in
//     the soliciting heartbeat's own stamp. An honest host can always clear it
//     by echoing the height that heartbeat carried. Judging against the landing
//     floor instead would fail honest acks whenever the floor rose in between,
//     which is also why landing late (§10.6) costs nothing.
func RefProducingNonce(diffNonce uint64, tx *types.DevshardTx) (uint64, bool) {
	if tx == nil {
		return 0, false
	}
	if tx.GetStartInference() != nil || tx.GetHeartbeat() != nil {
		return diffNonce, true
	}
	if c := tx.GetConfirmStart(); c != nil {
		return c.InferenceId, true
	}
	if f := tx.GetFinishInference(); f != nil {
		return f.InferenceId, true
	}
	if a := tx.GetHeightAck(); a != nil {
		return a.RefNonce + 1, true
	}
	return 0, false
}

// TxStamp returns the Diff-resident observed height on tx when the hash is
// present. Every such height is a reference height, so this is RefStamp without
// the hash.
func TxStamp(tx *types.DevshardTx) (uint64, bool) {
	h, _, ok := RefStamp(tx)
	return h, ok
}

// SequencerComposed reports whether tx is user-signed (MsgHeartbeat /
// MsgStartInference). Those stamps never raise F (spec §14 rule 3) and must
// not raise the turn clock.
func SequencerComposed(tx *types.DevshardTx) bool {
	if tx == nil {
		return false
	}
	return tx.GetStartInference() != nil || tx.GetHeartbeat() != nil
}

// HostSignedStamp is a Diff-resident height bound to a host key: ack, confirm,
// or finish. Heartbeat and start are excluded — they are user claims.
func HostSignedStamp(tx *types.DevshardTx) (uint64, bool) {
	if tx == nil {
		return 0, false
	}
	if tx.GetHeightAck() == nil && tx.GetConfirmStart() == nil && tx.GetFinishInference() == nil {
		return 0, false
	}
	return TxStamp(tx)
}

// LogResidentHeight is the clock TurnTracker.Observe must use: the highest
// *host-signed* Diff-resident stamp in txs, else lastCompleted (h_last). User
// heartbeats and starts are claims, not turn time — feeding them to
// windowClosed would let one sequencer stamp close every open ack window.
// A live oracle read is never a legal input — turn state is a pure function
// of the log.
func LogResidentHeight(txs []*types.DevshardTx, lastCompleted uint64) uint64 {
	var h uint64
	for _, tx := range txs {
		if s, ok := HostSignedStamp(tx); ok && s > h {
			h = s
		}
	}
	if h == 0 {
		return lastCompleted
	}
	return h
}
