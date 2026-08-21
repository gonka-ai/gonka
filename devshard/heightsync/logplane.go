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
	// there is nothing in Diff for it to adjudicate. Strong (§8 / §15) sharpens
	// the refusal into a proof obligation without widening its scope.
	ErrStrongRequired = errors.New("INVALID(strong_required)")
)

// MaxObservedBlockHashBytes is Tendermint SHA-256. Empty remains legal
// (StampPresent is false); only an upper bound is enforced so short test
// hashes still pass L1.
const MaxObservedBlockHashBytes = 32

// MaxMainnetBlockHashHexChars is MaxObservedBlockHashBytes as unprefixed hex.
const MaxMainnetBlockHashHexChars = MaxObservedBlockHashBytes * 2

// MaxOriginSignatureBytes is a recoverable secp256k1 signature (R‖S‖recid).
const MaxOriginSignatureBytes = 65

// LogPlaneInput is one diff presented to CheckDiffLogPlane.
type LogPlaneInput struct {
	Nonce        uint64
	Txs          []*types.DevshardTx
	Sec          *HeightSyncSection // nil skips L4 and L5a (replay / catch-up / gossip)
	LocalAligned uint64             // verifier tip for L5a; 0 skips the live band
	Oracle       blocks.BlockOracle // L6; nil leaves pairs pending
	RequestLeg   *RequestLegEvidence
	// Floor lets L4 check the full producer identity max(anchor, F(m)) rather
	// than the weaker "at least the anchor", and lets L6 tell a carried pair
	// from a first-party one. CheckDiffLogPlane fills it from LogPlaneState;
	// transport-edge callers that have no log view leave it nil.
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

	hbs, acks := collectLogPlaneTxs(in.Nonce, in.Txs)

	if err := checkL1(in.Nonce, hbs, acks, st); err != nil {
		logLogPlane(in.Nonce, 0, "L1", "INVALID", err.Error(), "escrow", st.EscrowID)
		return out.invalid(err, "bad_framing")
	}
	if err := checkL2(acks, st); err != nil {
		logLogPlane(in.Nonce, 0, "L2", "INVALID", err.Error(), "escrow", st.EscrowID)
		return out.invalid(err, "ack_sig_invalid")
	}
	if err := checkL3(hbs, acks, st); err != nil {
		logLogPlane(in.Nonce, 0, "L3", "INVALID", err.Error(), "escrow", st.EscrowID)
		return out.invalid(err, "ack_causality")
	}
	if err := checkL0(in.Nonce, in.Txs, st); err != nil {
		return out.invalid(err, "height_regression")
	}
	if err := checkL0b(in.Nonce, in.Txs, st); err != nil {
		return out.invalid(err, "height_regression")
	}

	checkL7(hbs, acks, st.Tracker, in.Nonce, &out)

	if in.Floor == nil {
		in.Floor = st.Floor
	}
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
	hbs, acks := collectLogPlaneTxs(in.Nonce, in.Txs)
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

func collectLogPlaneTxs(diffNonce uint64, txs []*types.DevshardTx) ([]heartbeatRef, []ackRef) {
	var hbs []heartbeatRef
	var acks []ackRef
	for _, tx := range txs {
		if tx == nil {
			continue
		}
		if hb := tx.GetHeartbeat(); hb != nil {
			hbs = append(hbs, heartbeatRef{nonce: diffNonce, hb: hb})
		}
		if ack := tx.GetHeightAck(); ack != nil {
			acks = append(acks, ackRef{nonce: diffNonce, ack: ack})
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
		if len(hb.ObservedBlockHash) > MaxObservedBlockHashBytes {
			return fmt.Errorf("%w: observed_block_hash len %d", ErrBadFraming, len(hb.ObservedBlockHash))
		}
		if len(hb.SyncVector) > int(st.SlotsNum) {
			return fmt.Errorf("%w: sync_vector len %d for slots_num %d", ErrBadFraming, len(hb.SyncVector), st.SlotsNum)
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
		n := len(ack.PeerSeen)
		if 8*n < int(st.SlotsNum) || n > peerSeenMaxBytes(st.SlotsNum) {
			return fmt.Errorf("%w: peer_seen len %d for slots_num %d", ErrBadFraming, n, st.SlotsNum)
		}
		if len(ack.ObservedBlockHash) > MaxObservedBlockHashBytes {
			return fmt.Errorf("%w: observed_block_hash len %d", ErrBadFraming, len(ack.ObservedBlockHash))
		}
	}
	return nil
}

func peerSeenMaxBytes(slots uint64) int {
	if slots == 0 {
		return 0
	}
	return int((slots + 7) / 8)
}

func checkL2(acks []ackRef, st LogPlaneState) error {
	if st.Verifier == nil {
		if len(acks) == 0 {
			return nil
		}
		return fmt.Errorf("%w: verifier required", ErrAckSigInvalid)
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

func checkL3(hbs []heartbeatRef, acks []ackRef, st LogPlaneState) error {
	inDiff := make(map[uint64]uint64, len(hbs))
	for _, ref := range hbs {
		// A Diff may carry more than one heartbeat (batched span). Keep the
		// first so an ack of that heartbeat's nonce still resolves.
		if _, exists := inDiff[ref.nonce]; exists {
			continue
		}
		inDiff[ref.nonce] = ref.hb.TurnSeq
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
// carries no false positives. Both branches stay available however the floor
// moved, because the floor only ever rises to a height the log already holds:
// how far it may rise on one signer's word is FloorIndex's business, not this
// check's.
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
			err := fmt.Errorf("%w: %s height %d < floor %d as of nonce %d",
				ErrHeightRegression, refLegName(tx), h, floor, m)
			// Honest producers lift to F(m) or omit, so this firing is a
			// shipping bug, a stale-floor replica, or authored misbehaviour.
			// The nonce is not consumed; retries of the same payload will
			// keep failing until the stamp is lifted or omitted.
			logLogPlane(nonce, 0, "L0", "INVALID", err.Error(),
				"escrow", st.EscrowID,
				LogFieldLeg, refLegName(tx),
				LogFieldHeight, h,
				LogFieldFloor, floor,
				LogFieldProducingNonce, m,
			)
			return err
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
// Presence is keyed on hash; unstamped legs are skipped (spec §14).
func checkL0b(nonce uint64, txs []*types.DevshardTx, st LogPlaneState) error {
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
			err := fmt.Errorf("%w: inference %d finish %d < confirm %d", ErrHeightRegression, id, s.finish, s.confirm)
			logLogPlane(nonce, 0, "L0b", "INVALID", err.Error(),
				"escrow", st.EscrowID,
				LogFieldLeg, "finish",
				LogFieldHeight, s.finish,
				"confirm_height", s.confirm,
				"inference_id", id,
			)
			return err
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

func checkL7(hbs []heartbeatRef, acks []ackRef, tracker *TurnTracker, nonce uint64, out *LogPlaneResult) {
	acksByTurn := make(map[uint64]map[uint32]AckRecord, len(acks))
	for _, ref := range acks {
		if ref.ack == nil {
			continue
		}
		m := acksByTurn[ref.ack.TurnSeq]
		if m == nil {
			m = make(map[uint32]AckRecord)
			acksByTurn[ref.ack.TurnSeq] = m
		}
		m[ref.ack.SlotId] = AckRecord{
			Nonce:     nonce,
			Height:    ref.ack.ObservedHeight,
			Hash:      append([]byte(nil), ref.ack.ObservedBlockHash...),
			SyncState: ref.ack.SyncState,
		}
	}
	for _, ref := range hbs {
		if len(ref.hb.SyncVector) == 0 {
			continue
		}
		logAcks := map[uint32]AckRecord{}
		if prev := tracker.Record(ref.hb.TurnSeq - 1); prev != nil {
			logAcks = prev.Acks
		}
		if extra := acksByTurn[ref.hb.TurnSeq-1]; extra != nil {
			for slot, a := range extra {
				logAcks[slot] = a
			}
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
// Request leg / heartbeat: the section is the sequencer's own first-party read
// while the heartbeat is a reference height, so the two are tied by the same
// producer rule as the response leg — max(anchor, F(m)) — and not by equality.
// Strict equality named every lagging honest sequencer the author of a carrier
// dispute, because lifting to a floor above its own tip is what L0 obliges it
// to do. What survives is the half that is a genuine self-contradiction: a
// heartbeat *below* the height its own signed envelope reports.
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
	// A carry-forward section relays some peer's tip, so the sequencer's own
	// reading is not on the wire at all and there is nothing first-party left to
	// bind the heartbeat against. Evading the check this way costs an attacker
	// nothing it did not already have: the only thing it dodges is understating
	// against its own envelope, which a self-consistent liar never does.
	if isCarryForwardAnchor(sec) {
		return
	}
	for _, ref := range hbs {
		if !StampPresent(ref.hb.ObservedBlockHash) {
			continue
		}
		m, _ := RefProducingNonce(in.Nonce, &types.DevshardTx{
			Tx: &types.DevshardTx_Heartbeat{Heartbeat: ref.hb},
		})
		want, exact := expect(m)
		if !bad(ref.hb.ObservedHeight, want, exact) {
			continue
		}
		var blob, sig []byte
		var ts int64
		escrow := ""
		if in.RequestLeg != nil {
			// Digest, not the HTTP body: CanonicalRequestLegBytes is already
			// what the signature covers (spec §15 request leg).
			blob = CanonicalRequestLegBytes(in.RequestLeg.EscrowID, in.RequestLeg.Body, in.RequestLeg.Timestamp)
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
			Detail: fmt.Sprintf("heartbeat height %d != max(request section %d, floor) = %d",
				ref.hb.ObservedHeight, envH, want),
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

// checkL6 reconciles a Diff-resident (height, hash) against the verifier's own
// follower and attributes what it finds.
//
// Attribution is the point, not detection. The producer rule obliges a party
// behind the floor to carry F(m), so an unreconcilable pair spreads: every
// honest carrier repeats it. A verifier holds the log, so it can separate the
// two exactly — a pair identical to F(m) is the floor being echoed — and the
// mark then names the signer that established that floor. Blame staying with the
// originator is what makes lifting to the floor safe for the party lifting.
func checkL6(ctx context.Context, in LogPlaneInput, hbs []heartbeatRef, acks []ackRef, out *LogPlaneResult) {
	if in.Oracle == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	check := func(h uint64, hash []byte, slot uint32, turn, producedAt uint64) {
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
		if origin, ok := carriedFrom(in.Floor, producedAt, h, hash); ok {
			m.Origin = FloorAuthorLabel(origin.Author)
			m.OriginNonce = origin.Nonce
			m.Detail += fmt.Sprintf("; carried from F(%d), set by %s at nonce %d",
				producedAt, m.Origin, origin.Nonce)
		}
		out.DeferredFails = append(out.DeferredFails, m)
		out.mark(m)
		logLogPlane(in.Nonce, slot, "L6", "DEFERRED_FAIL", m.Detail)
	}
	for _, ref := range hbs {
		check(ref.hb.ObservedHeight, ref.hb.ObservedBlockHash, 0, ref.hb.TurnSeq, in.Nonce)
	}
	for _, ref := range acks {
		check(ref.ack.ObservedHeight, ref.ack.ObservedBlockHash, ref.ack.SlotId, ref.ack.TurnSeq,
			ref.ack.RefNonce+1)
	}
}

// carriedFrom reports the floor a stamp reproduces exactly, if any. Equality on
// both height and hash is the test: the producer rule offers a carrier no other
// value, and a party inventing a pair cannot land on one already in the log.
func carriedFrom(floor *FloorIndex, producedAt, h uint64, hash []byte) (FloorPoint, bool) {
	if floor == nil {
		return FloorPoint{}, false
	}
	p, known := floor.PointAsOf(producedAt)
	if !known || p.Height != h || !bytes.Equal(p.Hash, hash) {
		return FloorPoint{}, false
	}
	return p, true
}

func absU64(a, b uint64) uint64 {
	if a > b {
		return a - b
	}
	return b - a
}

func logLogPlane(nonce uint64, slot uint32, check, verdict, reason string, extra ...any) {
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
	kvs = append(kvs, extra...)
	switch verdict {
	case "INVALID":
		// L0–L3 reject the whole diff and do not consume the nonce. A
		// producer that keeps sending the same payload will loop on this
		// line until it lifts to F(m) or omits.
		logging.Error("heightsync: logplane", kvs...)
	default:
		logging.Debug("heightsync: logplane", kvs...)
	}
}
