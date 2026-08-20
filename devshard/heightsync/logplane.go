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
	// ErrStrongRequired belongs to the transport plane only, where refusing an
	// exchange is a local admission decision (L5a). The log plane has no
	// counterpart: divergence between followers is monitoring, permanently, so
	// there is nothing in Diff for it to adjudicate. Phase F sharpens the
	// refusal into a proof obligation without widening its scope.
	ErrStrongRequired = errors.New("INVALID(strong_required)")
)

// LogPlaneInput is one diff presented to CheckDiffLogPlane.
type LogPlaneInput struct {
	Nonce        uint64
	Txs          []*types.DevshardTx
	Sec          *HeightSyncSection // nil skips L4 and L5a (replay / catch-up / gossip)
	LocalAligned uint64             // verifier tip for L5a; 0 skips the live band
	Oracle       blocks.BlockOracle // L6; nil leaves pairs pending
	RequestLeg   *RequestLegEvidence
	// Floor lets L4 check the full producer identity max(anchor, F(m)) rather
	// than the weaker "at least the anchor". CheckDiffLogPlane fills it from
	// LogPlaneState; transport-edge callers that have no log view leave it nil.
	Floor *FloorIndex
}

// LogPlaneState is frozen verifier state from already-accepted diffs.
type LogPlaneState struct {
	SlotsNum uint64
	SlotKeys map[uint32]string
	Verifier signing.Verifier
	Tracker  *TurnTracker
	// Floor answers F(m) for L0. Nil disables the check.
	Floor    *FloorIndex
	Cfg      HeartbeatConfig
	EscrowID string
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
// Pure Diff (always): L0, L0b, L1, L2, L3, L7 — L0–L3 may INVALID.
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
	if err := checkL0(in.Nonce, in.Txs, st); err != nil {
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

	checkL7(hbs, scratch, in.Nonce, &out)

	if in.Sec != nil {
		if in.Floor == nil {
			in.Floor = st.Floor
		}
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

// checkL0 enforces reference-height monotonicity against the floor the stamp's
// *producer* could have known.
//
// Scope is every Diff-resident height, heartbeats and acks included: the log has
// one height semantics, not two (spec §14). Basis is F(producing nonce), not
// F(landing nonce) — see RefProducingNonce for how each message type names it.
//
// An honest producer can always satisfy this — it stamps max(own_tip, F(m)) or
// omits the stamp entirely — so a violation is real misbehaviour and INVALID
// carries no false positives.
func checkL0(nonce uint64, txs []*types.DevshardTx, st LogPlaneState) error {
	if st.Floor == nil {
		return nil
	}
	for _, tx := range txs {
		h, _, ok := RefStamp(tx)
		if !ok {
			continue
		}
		m, ok := RefProducingNonce(nonce, tx)
		if !ok {
			continue
		}
		floor, _, known := st.Floor.AsOf(m)
		if !known || floor == 0 {
			continue
		}
		if h < floor {
			return fmt.Errorf("%w: %s height %d < floor %d as of nonce %d",
				ErrHeightRegression, refLegName(tx), h, floor, m)
		}
	}
	return nil
}

func refLegName(tx *types.DevshardTx) string {
	switch {
	case tx.GetStartInference() != nil:
		return "start"
	case tx.GetConfirmStart() != nil:
		return "confirm"
	case tx.GetFinishInference() != nil:
		return "finish"
	case tx.GetHeartbeat() != nil:
		return "heartbeat"
	case tx.GetHeightAck() != nil:
		return "ack"
	}
	return "stamp"
}

// checkL0b keeps the one per-inference ordering that is same-signer.
//
// confirm and finish are both produced by the executor, so confirm ≤ finish is
// genuine per-signer monotonicity. start is user-signed and carries a possibly
// higher carried reference, so start-vs-confirm and start-vs-finish are
// cross-signer comparisons: an executor legitimately behind the height the user
// carried is not a regression, and those pairs are deliberately not checked.
//
// Presence is keyed on hash; unstamped legs are skipped (H38).
func checkL0b(txs []*types.DevshardTx) error {
	type infStamps struct {
		confirm, finish       uint64
		hasConfirm, hasFinish bool
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
		if s.hasConfirm && s.hasFinish && s.finish < s.confirm {
			return fmt.Errorf("%w: inference %d finish %d < confirm %d", ErrHeightRegression, id, s.finish, s.confirm)
		}
	}
	return nil
}

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

// checkL4 binds the Diff-resident height to the height in the envelope of the
// same exchange. The two legs are bound differently because they are asymmetric.
//
// Request leg / heartbeat: the sequencer is a carrier on both planes, so the
// section and the heartbeat are the same carried value and must be *equal*. A
// mismatch is a self-contradiction under one identity.
//
// Response leg / ack: the section anchor is the host's first-party oracle read
// while the ack is a reference height, so they differ by design and the producer
// rule ties them as max(anchor, F(ref_nonce)). A receiver holds the anchor and
// the log, so it can evaluate that identity exactly — catching a host that
// understates its tip in the anchor *or* overstates its reference height in the
// log. Without a floor view (transport-edge callers) it degrades to the
// floor-free half, ack >= anchor, which is all that is checkable there.
func checkL4(in LogPlaneInput, hbs []heartbeatRef, acks []ackRef, out *LogPlaneResult) {
	sec := in.Sec
	if !IsAnchorSection(sec) {
		return
	}
	envH := uint64(sec.MainnetHeight)
	// expect reports the height the producer rule demands, and whether the
	// bound is exact. m is the producing nonce of the leg being checked.
	expect := func(m uint64) (uint64, bool) {
		if in.Floor == nil {
			return envH, false
		}
		floor, _, known := in.Floor.AsOf(m)
		if !known {
			return envH, false
		}
		if floor > envH {
			return floor, true
		}
		return envH, true
	}
	bad := func(h, want uint64, exact bool) bool {
		if exact {
			return h != want
		}
		return h < want
	}
	if sec.Direction == "response" {
		for _, ref := range acks {
			if !StampPresent(ref.ack.ObservedBlockHash) {
				continue
			}
			m, _ := RefProducingNonce(in.Nonce, &types.DevshardTx{
				Tx: &types.DevshardTx_HeightAck{HeightAck: ref.ack},
			})
			want, exact := expect(m)
			if !bad(ref.ack.ObservedHeight, want, exact) {
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
				Detail: fmt.Sprintf("ack height %d != max(response section %d, floor) = %d",
					ref.ack.ObservedHeight, envH, want),
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
