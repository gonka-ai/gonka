package heightsync

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"

	"devshard/chainoracle/blocks"
	"devshard/logging"
	"devshard/signing"
	"devshard/types"
)

var (
	ErrHeightRegression = errors.New("INVALID(height_regression)")
	ErrBadFraming       = errors.New("INVALID(bad_framing)")
	ErrAckSigInvalid    = errors.New("INVALID(ack_sig_invalid)")
	ErrAckCausality     = errors.New("INVALID(ack_causality)")
	ErrStrongRequired   = errors.New("INVALID(strong_required)")
)

// LogPlaneInput is one diff presented to CheckDiffLogPlane.
type LogPlaneInput struct {
	Nonce        uint64
	Txs          []*types.DevshardTx
	Sec          *HeightSyncSection // nil skips L4 and L5a (replay / catch-up / gossip)
	LocalAligned uint64             // verifier tip for L5a; 0 skips the live band
	Oracle       blocks.BlockOracle // L6; nil leaves pairs pending
	RequestLeg   *RequestLegEvidence
}

// LogPlaneState is frozen verifier state from already-accepted diffs.
type LogPlaneState struct {
	SlotsNum       uint64
	SlotKeys       map[uint32]string
	Verifier       signing.Verifier
	Tracker        *TurnTracker
	MaxStampHeight uint64
	Cfg            HeartbeatConfig
	EscrowID       string
}

// LogPlaneResult is the replay-stable verdict plus edge/deferred marks.
type LogPlaneResult struct {
	Err           error
	Reason        string
	Marks         []AttributableMark
	DeferredFails []AttributableMark
}

func (r LogPlaneResult) invalid(err error, reason string) LogPlaneResult {
	r.Err = err
	r.Reason = reason
	return r
}

func (r *LogPlaneResult) mark(m AttributableMark) {
	r.Marks = append(r.Marks, m)
}

// CheckDiffLogPlane runs L0–L7 against one diff.
//
// Pure Diff (always): L0, L0b, L1, L2, L3, L5b, L7 — L0–L3/L5b may INVALID.
// Edge (sec != nil): L4, L5a — mark only.
// Deferred: L6 — DEFERRED_FAIL once the oracle has block H.
func CheckDiffLogPlane(ctx context.Context, in LogPlaneInput, st LogPlaneState) LogPlaneResult {
	st.Cfg = st.Cfg.withDefaults()
	if st.SlotsNum == 0 {
		st.SlotsNum = 1
	}
	var out LogPlaneResult

	hbs, acks := collectLogPlaneTxs(in.Txs)

	if err := checkL1(in.Nonce, hbs, acks, st); err != nil {
		logLogPlane(in.Nonce, 0, "L1", "INVALID", err.Error())
		return out.invalid(err, "bad_framing")
	}
	if err := checkL2(acks, st); err != nil {
		logLogPlane(in.Nonce, 0, "L2", "INVALID", err.Error())
		return out.invalid(err, "ack_sig_invalid")
	}
	if err := checkL3(in.Nonce, hbs, acks, st); err != nil {
		logLogPlane(in.Nonce, 0, "L3", "INVALID", err.Error())
		return out.invalid(err, "ack_causality")
	}
	if err := checkL0(in.Nonce, in.Txs, hbs, acks, st); err != nil {
		logLogPlane(in.Nonce, 0, "L0", "INVALID", err.Error())
		return out.invalid(err, "height_regression")
	}
	if err := checkL0b(in.Txs); err != nil {
		logLogPlane(in.Nonce, 0, "L0b", "INVALID", err.Error())
		return out.invalid(err, "height_regression")
	}

	scratch := st.Tracker.Clone()
	if scratch == nil {
		scratch = NewTurnTracker(st.SlotsNum, 0, st.Cfg)
	}
	hNow := maxStampInTxs(hbs, acks)
	if hNow == 0 {
		hNow = scratch.LastCompletedHeight()
	}
	scratch.Observe(in.Nonce, in.Txs, hNow)

	if err := checkL5b(acks, scratch, st.Cfg); err != nil {
		logLogPlane(in.Nonce, 0, "L5b", "INVALID", err.Error())
		return out.invalid(err, "strong_required")
	}
	checkL7(hbs, scratch, in.Nonce, &out)

	if in.Sec != nil {
		checkL4(in, hbs, acks, &out)
		checkL5a(in, hbs, acks, st.Cfg, &out)
	}
	checkL6(ctx, in, hbs, acks, &out)

	if out.Err == nil {
		logLogPlane(in.Nonce, 0, "ok", "OK", "")
	}
	return out
}

// CheckEnvelopeBinding runs L4 and L5a only. Used at the transport edge where
// the HeightSyncSection is still attached. Never INVALID-ates a diff.
func CheckEnvelopeBinding(in LogPlaneInput, cfg HeartbeatConfig) []AttributableMark {
	if in.Sec == nil {
		return nil
	}
	cfg = cfg.withDefaults()
	var out LogPlaneResult
	hbs, acks := collectLogPlaneTxs(in.Txs)
	checkL4(in, hbs, acks, &out)
	checkL5a(in, hbs, acks, cfg, &out)
	return out.Marks
}

type heartbeatRef struct {
	nonce uint64
	hb    *types.MsgHeartbeat
}

type ackRef struct {
	nonce uint64
	ack   *types.MsgHeightAck
}

func collectLogPlaneTxs(txs []*types.DevshardTx) ([]heartbeatRef, []ackRef) {
	var hbs []heartbeatRef
	var acks []ackRef
	for _, tx := range txs {
		if tx == nil {
			continue
		}
		if hb := tx.GetHeartbeat(); hb != nil {
			hbs = append(hbs, heartbeatRef{hb: hb})
		}
		if ack := tx.GetHeightAck(); ack != nil {
			acks = append(acks, ackRef{ack: ack})
		}
	}
	return hbs, acks
}

func checkL1(nonce uint64, hbs []heartbeatRef, acks []ackRef, st LogPlaneState) error {
	_ = nonce
	prevTurn := uint64(0)
	if st.Tracker != nil {
		prevTurn = st.Tracker.MaxTurnSeq()
	}
	for _, ref := range hbs {
		hb := ref.hb
		if hb.TurnSeq == 0 {
			return fmt.Errorf("%w: turn_seq 0", ErrBadFraming)
		}
		if prevTurn > 0 && hb.TurnSeq < prevTurn {
			return fmt.Errorf("%w: turn_seq %d < %d", ErrBadFraming, hb.TurnSeq, prevTurn)
		}
		if hb.SlotsNum != st.SlotsNum {
			return fmt.Errorf("%w: slots_num %d != group %d", ErrBadFraming, hb.SlotsNum, st.SlotsNum)
		}
	}
	for _, ref := range acks {
		ack := ref.ack
		if ack.TurnSeq == 0 {
			return fmt.Errorf("%w: ack turn_seq 0", ErrBadFraming)
		}
		if uint64(ack.SlotId) >= st.SlotsNum {
			return fmt.Errorf("%w: slot %d >= %d", ErrBadFraming, ack.SlotId, st.SlotsNum)
		}
		if 8*len(ack.PeerSeen) < int(st.SlotsNum) {
			return fmt.Errorf("%w: peer_seen len %d for slots_num %d", ErrBadFraming, len(ack.PeerSeen), st.SlotsNum)
		}
	}
	return nil
}

func checkL2(acks []ackRef, st LogPlaneState) error {
	if st.Verifier == nil {
		return nil
	}
	for _, ref := range acks {
		key, ok := st.SlotKeys[ref.ack.SlotId]
		if !ok || key == "" {
			return fmt.Errorf("%w: no key for slot %d", ErrAckSigInvalid, ref.ack.SlotId)
		}
		if err := VerifyAck(st.Verifier, ref.ack, key); err != nil {
			return fmt.Errorf("%w: %v", ErrAckSigInvalid, err)
		}
	}
	return nil
}

func checkL3(diffNonce uint64, hbs []heartbeatRef, acks []ackRef, st LogPlaneState) error {
	inDiff := make(map[uint64]uint64, len(hbs))
	for _, ref := range hbs {
		inDiff[diffNonce] = ref.hb.TurnSeq
	}
	for _, ref := range acks {
		ack := ref.ack
		turn, ok := inDiff[ack.RefNonce]
		if !ok && st.Tracker != nil {
			turn, ok = st.Tracker.HeartbeatTurn(ack.RefNonce)
		}
		if !ok {
			return fmt.Errorf("%w: ref_nonce %d has no heartbeat", ErrAckCausality, ack.RefNonce)
		}
		if turn != ack.TurnSeq {
			return fmt.Errorf("%w: ref_nonce %d turn %d != ack turn %d", ErrAckCausality, ack.RefNonce, turn, ack.TurnSeq)
		}
	}
	return nil
}

func checkL0(nonce uint64, txs []*types.DevshardTx, hbs []heartbeatRef, acks []ackRef, st LogPlaneState) error {
	_ = nonce
	floor := st.MaxStampHeight
	check := func(h uint64, present bool, who string) error {
		if !present {
			return nil
		}
		if floor > 0 && h < floor {
			return fmt.Errorf("%w: %s height %d < %d", ErrHeightRegression, who, h, floor)
		}
		return nil
	}
	for _, ref := range hbs {
		if err := check(ref.hb.ObservedHeight, StampPresent(ref.hb.ObservedBlockHash), "heartbeat"); err != nil {
			return err
		}
	}
	for _, ref := range acks {
		if err := check(ref.ack.ObservedHeight, StampPresent(ref.ack.ObservedBlockHash), "ack"); err != nil {
			return err
		}
	}
	for _, tx := range txs {
		if tx == nil {
			continue
		}
		if start := tx.GetStartInference(); start != nil {
			if h, ok := inferenceStamp(start); ok {
				if err := check(h, true, "start"); err != nil {
					return err
				}
			}
		}
		if conf := tx.GetConfirmStart(); conf != nil {
			if h, ok := inferenceStamp(conf); ok {
				if err := check(h, true, "confirm"); err != nil {
					return err
				}
			}
		}
		if fin := tx.GetFinishInference(); fin != nil {
			if h, ok := inferenceStamp(fin); ok {
				if err := check(h, true, "finish"); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func checkL0b(txs []*types.DevshardTx) error {
	// E7 stamps on Start/Confirm/Finish are not on the wire yet. Presence is
	// keyed on hash; unstamped legs are skipped (H38).
	type infStamps struct {
		start, confirm, finish uint64
		hasStart, hasConfirm, hasFinish bool
	}
	byID := make(map[uint64]*infStamps)
	stamp := func(id uint64) *infStamps {
		s := byID[id]
		if s == nil {
			s = &infStamps{}
			byID[id] = s
		}
		return s
	}
	for _, tx := range txs {
		if tx == nil {
			continue
		}
		if start := tx.GetStartInference(); start != nil {
			if h, ok := inferenceStamp(start); ok {
				s := stamp(start.InferenceId)
				s.hasStart, s.start = true, h
			}
		}
		if conf := tx.GetConfirmStart(); conf != nil {
			if h, ok := inferenceStamp(conf); ok {
				s := stamp(conf.InferenceId)
				s.hasConfirm, s.confirm = true, h
			}
		}
		if fin := tx.GetFinishInference(); fin != nil {
			if h, ok := inferenceStamp(fin); ok {
				s := stamp(fin.InferenceId)
				s.hasFinish, s.finish = true, h
			}
		}
	}
	for id, s := range byID {
		if s.hasStart && s.hasConfirm && s.confirm < s.start {
			return fmt.Errorf("%w: inference %d confirm %d < start %d", ErrHeightRegression, id, s.confirm, s.start)
		}
		if s.hasConfirm && s.hasFinish && s.finish < s.confirm {
			return fmt.Errorf("%w: inference %d finish %d < confirm %d", ErrHeightRegression, id, s.finish, s.confirm)
		}
		if s.hasStart && s.hasFinish && s.finish < s.start {
			return fmt.Errorf("%w: inference %d finish %d < start %d", ErrHeightRegression, id, s.finish, s.start)
		}
	}
	return nil
}

// inferenceStamp is a hook for E7. Until those proto fields exist, nothing
// reports a present stamp and L0b is a no-op.
func inferenceStamp(msg any) (uint64, bool) {
	type stamped interface {
		GetObservedHeight() uint64
		GetObservedBlockHash() []byte
	}
	s, ok := msg.(stamped)
	if !ok {
		return 0, false
	}
	if !StampPresent(s.GetObservedBlockHash()) {
		return 0, false
	}
	return s.GetObservedHeight(), true
}

func checkL5b(acks []ackRef, tracker *TurnTracker, cfg HeartbeatConfig) error {
	d := cfg.DeltaBlocks
	for _, ref := range acks {
		ack := ref.ack
		if ack.SyncState != types.SyncState_SYNCED {
			continue
		}
		if !StampPresent(ack.ObservedBlockHash) {
			continue
		}
		rec := tracker.Record(ack.TurnSeq)
		if rec == nil || rec.HReq == 0 {
			continue
		}
		if absU64(ack.ObservedHeight, rec.HReq) > d {
			return fmt.Errorf("%w: SYNCED ack height %d outside D=%d of h_req %d",
				ErrStrongRequired, ack.ObservedHeight, d, rec.HReq)
		}
	}
	return nil
}

func checkL7(hbs []heartbeatRef, tracker *TurnTracker, nonce uint64, out *LogPlaneResult) {
	for _, ref := range hbs {
		if len(ref.hb.SyncVector) == 0 {
			continue
		}
		var logAcks map[uint32]AckRecord
		if prev := tracker.Record(ref.hb.TurnSeq - 1); prev != nil {
			logAcks = prev.Acks
		} else {
			logAcks = map[uint32]AckRecord{}
		}
		for _, c := range CheckVectorAgainstLog(ref.hb.SyncVector, logAcks) {
			out.mark(AttributableMark{
				Kind:    MarkVectorContradiction,
				Slot:    c.Slot,
				TurnSeq: ref.hb.TurnSeq,
				Nonce:   nonce,
				Detail:  fmt.Sprintf("ACKED nonce=%d h=%d missing from log", c.ClaimedNonce, c.ClaimedH),
			})
			logLogPlane(nonce, c.Slot, "L7", "MARK", "vector_contradiction")
		}
	}
}

func checkL4(in LogPlaneInput, hbs []heartbeatRef, acks []ackRef, out *LogPlaneResult) {
	sec := in.Sec
	if !IsAnchorSection(sec) {
		return
	}
	envH := uint64(sec.MainnetHeight)
	if sec.Direction == "response" {
		for _, ref := range acks {
			if !StampPresent(ref.ack.ObservedBlockHash) {
				continue
			}
			if ref.ack.ObservedHeight == envH {
				continue
			}
			blob, _ := CanonicalOriginBytes(sec)
			out.mark(AttributableMark{
				Kind:    MarkDisputeOriginator,
				Slot:    ref.ack.SlotId,
				TurnSeq: ref.ack.TurnSeq,
				Nonce:   in.Nonce,
				Blob:    blob,
				Sig:     append([]byte(nil), sec.SenderSignature...),
				Detail:  fmt.Sprintf("ack height %d != response section %d", ref.ack.ObservedHeight, envH),
			})
			logLogPlane(in.Nonce, ref.ack.SlotId, "L4", "MARK", "dispute_originator")
		}
		return
	}
	for _, ref := range hbs {
		if !StampPresent(ref.hb.ObservedBlockHash) {
			continue
		}
		if ref.hb.ObservedHeight == envH {
			continue
		}
		var blob, sig []byte
		var ts int64
		escrow := ""
		if in.RequestLeg != nil {
			blob = in.RequestLeg.Body
			sig = in.RequestLeg.Sig
			ts = in.RequestLeg.Timestamp
			escrow = in.RequestLeg.EscrowID
		}
		out.mark(AttributableMark{
			Kind:      MarkDisputeCarrier,
			TurnSeq:   ref.hb.TurnSeq,
			Nonce:     in.Nonce,
			Blob:      blob,
			Sig:       sig,
			EscrowID:  escrow,
			Timestamp: ts,
			Detail:    fmt.Sprintf("heartbeat height %d != request section %d", ref.hb.ObservedHeight, envH),
		})
		logLogPlane(in.Nonce, 0, "L4", "MARK", "dispute_carrier")
	}
}

func checkL5a(in LogPlaneInput, hbs []heartbeatRef, acks []ackRef, cfg HeartbeatConfig, out *LogPlaneResult) {
	if in.LocalAligned == 0 {
		return
	}
	d := cfg.DeltaBlocks
	check := func(h uint64, hash []byte, slot uint32, turn uint64, who string) {
		if !StampPresent(hash) {
			return
		}
		if absU64(h, in.LocalAligned) <= d {
			return
		}
		out.mark(AttributableMark{
			Kind:    MarkAdmissionDelta,
			Slot:    slot,
			TurnSeq: turn,
			Nonce:   in.Nonce,
			Detail:  fmt.Sprintf("%s height %d |Δ| vs local %d > D=%d", who, h, in.LocalAligned, d),
		})
		logLogPlane(in.Nonce, slot, "L5a", "MARK", "l5a_admission")
	}
	for _, ref := range hbs {
		check(ref.hb.ObservedHeight, ref.hb.ObservedBlockHash, 0, ref.hb.TurnSeq, "heartbeat")
	}
	for _, ref := range acks {
		check(ref.ack.ObservedHeight, ref.ack.ObservedBlockHash, ref.ack.SlotId, ref.ack.TurnSeq, "ack")
	}
}

func checkL6(ctx context.Context, in LogPlaneInput, hbs []heartbeatRef, acks []ackRef, out *LogPlaneResult) {
	if in.Oracle == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	check := func(h uint64, hash []byte, slot uint32, turn uint64) {
		if !StampPresent(hash) || h == 0 {
			return
		}
		hdr, err := in.Oracle.At(ctx, int64(h))
		if err != nil || hdr == nil {
			return // still pending
		}
		if bytes.Equal(hdr.BlockHash, hash) {
			return
		}
		m := AttributableMark{
			Kind:    MarkDeferredFail,
			Slot:    slot,
			TurnSeq: turn,
			Nonce:   in.Nonce,
			Blob:    append([]byte(nil), hash...),
			Detail:  fmt.Sprintf("hash at height %d != oracle %s", h, hex.EncodeToString(hdr.BlockHash)),
		}
		out.DeferredFails = append(out.DeferredFails, m)
		out.mark(m)
		logLogPlane(in.Nonce, slot, "L6", "DEFERRED_FAIL", m.Detail)
	}
	for _, ref := range hbs {
		check(ref.hb.ObservedHeight, ref.hb.ObservedBlockHash, 0, ref.hb.TurnSeq)
	}
	for _, ref := range acks {
		check(ref.ack.ObservedHeight, ref.ack.ObservedBlockHash, ref.ack.SlotId, ref.ack.TurnSeq)
	}
}

func maxStampInTxs(hbs []heartbeatRef, acks []ackRef) uint64 {
	var h uint64
	for _, ref := range hbs {
		if StampPresent(ref.hb.ObservedBlockHash) && ref.hb.ObservedHeight > h {
			h = ref.hb.ObservedHeight
		}
	}
	for _, ref := range acks {
		if StampPresent(ref.ack.ObservedBlockHash) && ref.ack.ObservedHeight > h {
			h = ref.ack.ObservedHeight
		}
	}
	return h
}

func absU64(a, b uint64) uint64 {
	if a > b {
		return a - b
	}
	return b - a
}

func logLogPlane(nonce uint64, slot uint32, check, verdict, reason string) {
	kvs := []any{
		LogFieldSubsystem, "heightsync",
		LogFieldNonce, nonce,
		LogFieldCheck, check,
		LogFieldVerdict, verdict,
	}
	if slot != 0 {
		kvs = append(kvs, "slot", slot)
	}
	if reason != "" {
		kvs = append(kvs, LogFieldReason, reason)
	}
	logging.Debug("heightsync: logplane", kvs...)
}
