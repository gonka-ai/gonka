package heightsync

import (
	"slices"

	"devshard/types"
)

// TurnState is the verifier-computed completion of one heartbeat turn.
type TurnState int

const (
	TurnOpen TurnState = iota
	TurnComplete
	TurnDegraded
)

func (s TurnState) String() string {
	switch s {
	case TurnOpen:
		return "open"
	case TurnComplete:
		return "complete"
	case TurnDegraded:
		return "degraded"
	default:
		return "unknown"
	}
}

// AckRecord is one host ack as folded into a SyncTurnRecord.
type AckRecord struct {
	Nonce     uint64
	Height    uint64
	Hash      []byte
	SyncState types.SyncState
	Late      bool
}

// SyncTurnRecord is the per-turn view every honest verifier computes from Diff.
type SyncTurnRecord struct {
	TurnSeq           uint64
	RequestSpan       [2]uint64 // [t, t+slots_num-1]
	HReq              uint64
	Acks              map[uint32]AckRecord
	State             TurnState
	CompletedAtHeight uint64
	// Reason is MsgHeartbeat.reason from the opening heartbeat (proposal §10.4).
	Reason string
}

// DefaultTurnRetain is how many turns the tracker keeps behind the latest,
// open ones included. L7 and repair read that tail. L3 uses heartbeatAt, which
// is not pruned with the turn record — one entry per heartbeat nonce, bounded
// by session length.
const DefaultTurnRetain uint64 = 64

// TurnTracker folds heartbeat + ack txs into SyncTurnRecords. Q is the same
// knob as (C-quorum); there is no second quorum parameter.
type TurnTracker struct {
	slotsNum        uint64
	quorum          int
	ackDeadline     uint64
	retain          uint64
	turns           map[uint64]*SyncTurnRecord
	heartbeatAt     map[uint64]uint64 // nonce → turn_seq (L3)
	stampHeight     uint64            // max inference-tx stamp; discharges h_last
	maxTurnSeq      uint64
	lastCompleted   uint64 // max CompletedAtHeight; survives prune
	lastCompleteSeq uint64
}

// NewTurnTracker constructs an empty tracker. quorum ≤ 0 uses QuorumForRoster(slotsNum).
func NewTurnTracker(slotsNum uint64, quorum int, cfg HeartbeatConfig) *TurnTracker {
	cfg = cfg.withDefaults()
	if slotsNum == 0 {
		slotsNum = 1
	}
	if quorum <= 0 {
		quorum = QuorumForRoster(int(slotsNum))
	}
	return &TurnTracker{
		slotsNum:    slotsNum,
		quorum:      quorum,
		ackDeadline: cfg.AckDeadlineBlocks,
		retain:      DefaultTurnRetain,
		turns:       make(map[uint64]*SyncTurnRecord),
		heartbeatAt: make(map[uint64]uint64),
	}
}

func (t *TurnTracker) Quorum() int {
	if t == nil {
		return 0
	}
	return t.quorum
}

func (t *TurnTracker) SlotsNum() uint64 {
	if t == nil {
		return 0
	}
	return t.slotsNum
}

// ArmingContext returns the highest complete turn_seq and degraded turn ids.
func (t *TurnTracker) ArmingContext() (lastComplete uint64, degraded []uint64) {
	if t == nil {
		return 0, nil
	}
	for seq, rec := range t.turns {
		if rec == nil {
			continue
		}
		if rec.State == TurnDegraded {
			degraded = append(degraded, seq)
		}
	}
	slices.Sort(degraded)
	return t.lastCompleteSeq, degraded
}

// Observe ingests one diff's txs at chain height hNow.
func (t *TurnTracker) Observe(diffNonce uint64, txs []*types.DevshardTx, hNow uint64) {
	if t == nil {
		return
	}
	for _, tx := range txs {
		if tx == nil {
			continue
		}
		if hb := tx.GetHeartbeat(); hb != nil {
			t.observeHeartbeat(diffNonce, hb)
		}
		if h, ok := inferenceTxStamp(tx); ok && h > t.stampHeight {
			t.stampHeight = h
		}
	}
	for _, tx := range txs {
		if tx == nil {
			continue
		}
		if ack := tx.GetHeightAck(); ack != nil {
			t.observeAck(diffNonce, ack)
		}
	}
	if t.stampHeight > hNow {
		hNow = t.stampHeight
	}
	t.AdvanceHeight(hNow)
}

func inferenceTxStamp(tx *types.DevshardTx) (uint64, bool) {
	if start := tx.GetStartInference(); start != nil {
		return inferenceStamp(start)
	}
	if conf := tx.GetConfirmStart(); conf != nil {
		return inferenceStamp(conf)
	}
	if fin := tx.GetFinishInference(); fin != nil {
		return inferenceStamp(fin)
	}
	return 0, false
}

func (t *TurnTracker) observeHeartbeat(nonce uint64, hb *types.MsgHeartbeat) {
	if t.heartbeatAt == nil {
		t.heartbeatAt = make(map[uint64]uint64)
	}
	t.heartbeatAt[nonce] = hb.TurnSeq
	t.noteTurnSeq(hb.TurnSeq)
	rec := t.turns[hb.TurnSeq]
	if rec == nil {
		slots := hb.SlotsNum
		if slots == 0 {
			slots = t.slotsNum
		}
		rec = &SyncTurnRecord{
			TurnSeq:     hb.TurnSeq,
			RequestSpan: [2]uint64{nonce, nonce + slots - 1},
			HReq:        heartbeatRequestHeight(hb),
			Acks:        make(map[uint32]AckRecord),
			State:       TurnOpen,
			Reason:      hb.Reason,
		}
		t.turns[hb.TurnSeq] = rec
		SetTurnState("open")
		return
	}
	if rec.Reason == "" && hb.Reason != "" {
		rec.Reason = hb.Reason
	}
	if nonce < rec.RequestSpan[0] {
		span := rec.RequestSpan[1] - rec.RequestSpan[0]
		rec.RequestSpan = [2]uint64{nonce, nonce + span}
	}
	if h := heartbeatRequestHeight(hb); h != 0 && (rec.HReq == 0 || h < rec.HReq) {
		rec.HReq = h
	}
}

// heartbeatRequestHeight is the height a heartbeat asks about, or zero.
//
// Presence is keyed on the hash everywhere else in the protocol (spec §14): a
// height without one is not a stamp, L0 skips it, and the floor ignores it. The
// turn window follows suit, because HReq is a minimum — admitting a hashless
// height would let it pin the whole turn's deadline low and cost every honest
// ack a `late` flag it cannot avoid.
func heartbeatRequestHeight(hb *types.MsgHeartbeat) uint64 {
	if hb == nil || hb.ObservedHeight == 0 || !StampPresent(hb.ObservedBlockHash) {
		return 0
	}
	return hb.ObservedHeight
}

func (t *TurnTracker) observeAck(nonce uint64, ack *types.MsgHeightAck) {
	t.noteTurnSeq(ack.TurnSeq)
	rec := t.turns[ack.TurnSeq]
	if rec == nil {
		rec = &SyncTurnRecord{
			TurnSeq: ack.TurnSeq,
			Acks:    make(map[uint32]AckRecord),
			State:   TurnOpen,
		}
		t.turns[ack.TurnSeq] = rec
		SetTurnState("open")
	}
	// Lateness is stamp-based, and the stamp is the host's: an ack carries the
	// height its author read when it composed the answer, which is the one
	// timestamp in the log the sequencer cannot forge or backdate. Comparing it
	// to h_req + D_ack asks whether the answer was composed while the request
	// still stood.
	late := rec.State == TurnDegraded
	if rec.HReq > 0 && ack.ObservedHeight > addSat(rec.HReq, t.ackDeadline) {
		late = true
	}
	existing, had := rec.Acks[ack.SlotId]
	if had {
		late = late || existing.Late
	}
	rec.Acks[ack.SlotId] = AckRecord{
		Nonce:     nonce,
		Height:    ack.ObservedHeight,
		Hash:      append([]byte(nil), ack.ObservedBlockHash...),
		SyncState: ack.SyncState,
		Late:      late,
	}
	if !had {
		IncAck(ack.SyncState.String(), late)
	}
}

// AdvanceHeight re-evaluates open turns against the current chain height.
func (t *TurnTracker) AdvanceHeight(hNow uint64) {
	if t == nil {
		return
	}
	for _, rec := range t.turns {
		t.recompute(rec, hNow)
	}
	t.prune()
}

func (t *TurnTracker) noteTurnSeq(seq uint64) {
	if seq > t.maxTurnSeq {
		t.maxTurnSeq = seq
	}
}

func (t *TurnTracker) prune() {
	if t.retain == 0 {
		t.retain = DefaultTurnRetain
	}
	var cutoff uint64
	if t.maxTurnSeq > t.retain {
		cutoff = t.maxTurnSeq - t.retain
	}
	for seq, rec := range t.turns {
		if rec == nil {
			continue
		}
		if seq >= cutoff {
			continue
		}
		delete(t.turns, seq)
	}
	// heartbeatAt is the L3 source of truth: ref_nonce names a heartbeat that
	// was in Diff, not a turn record still in RAM. Do not drop entries whose
	// turn has been pruned.
}

// windowClosed reports whether the turn's ack window has passed.
//
// The window is D_ack blocks wide starting at h_req, and D_ack is derived from
// the producer's own turnover budget (Interval + TurnTimeout) through the
// deployment's block time, so the log stops waiting just after the producer
// does — never while it is still legitimately collecting acks. The whole span
// plus the ack round trip lives inside that budget: heartbeats for one turn are
// composed together at one height, but the acks answering them are stamped as
// each host is reached, so their heights climb across the span. Judging them
// against a one-block window made honest acks late by construction.
func (t *TurnTracker) windowClosed(rec *SyncTurnRecord, hNow uint64) bool {
	if rec.HReq == 0 {
		return false
	}
	deadline := addSat(rec.HReq, t.ackDeadline)
	if hNow > deadline {
		return true
	}
	for _, a := range rec.Acks {
		if a.Height > deadline {
			return true
		}
	}
	return false
}

// countingAcks counts the acks that hold up this turn: any in-window ack from a
// distinct slot, whatever it says about its oracle.
//
// sync_state used to gate this, because completion fed (C-turn) and an
// ORACLE_UNAVAILABLE slot is no height witness. With (C-turn) withdrawn (spec
// §17) completion certifies only that Q slots were reachable and applying the
// log, which such a slot proves exactly as well as a SYNCED one — it echoes
// F(m) from the log it already has, and contributes no envelope anchor, so
// (C-quorum) is untouched. Excluding it instead made an honest host with a dead
// follower a permanent hole in the roster's cadence.
func (t *TurnTracker) countingAcks(rec *SyncTurnRecord) int {
	n := 0
	for _, a := range rec.Acks {
		if a.Late {
			continue
		}
		n++
	}
	return n
}

func (t *TurnTracker) recompute(rec *SyncTurnRecord, hNow uint64) {
	// A settled turn is history. Late acks never un-degrade (attack 22), and
	// they never un-complete either: a slot that already answered in time could
	// otherwise re-ack at a higher height, drag its own record past the deadline,
	// and pull the turn's count back below quorum after the fact.
	if rec.State != TurnOpen {
		return
	}
	counting := t.countingAcks(rec)
	if counting >= t.quorum {
		rec.State = TurnComplete
		rec.CompletedAtHeight = hNow
		if hNow > t.lastCompleted {
			t.lastCompleted = hNow
		}
		if rec.TurnSeq > t.lastCompleteSeq {
			t.lastCompleteSeq = rec.TurnSeq
		}
		IncHeartbeatTurn(rec.Reason, "complete")
		SetTurnState("complete")
		return
	}
	if t.windowClosed(rec, hNow) && counting < t.quorum {
		rec.State = TurnDegraded
		IncHeartbeatTurn(rec.Reason, "degraded")
		SetTurnState("degraded")
		return
	}
}

// Record returns a copy of the turn, or nil.
func (t *TurnTracker) Record(turnSeq uint64) *SyncTurnRecord {
	if t == nil {
		return nil
	}
	rec := t.turns[turnSeq]
	if rec == nil {
		return nil
	}
	return cloneTurn(rec)
}

// Latest is the highest turn_seq observed.
func (t *TurnTracker) Latest() *SyncTurnRecord {
	if t == nil || t.maxTurnSeq == 0 {
		return nil
	}
	rec := t.turns[t.maxTurnSeq]
	if rec == nil {
		return nil
	}
	return cloneTurn(rec)
}

// HeartbeatTurn reports the turn_seq of the MsgHeartbeat at nonce, if any.
func (t *TurnTracker) HeartbeatTurn(nonce uint64) (uint64, bool) {
	if t == nil {
		return 0, false
	}
	seq, ok := t.heartbeatAt[nonce]
	return seq, ok
}

// MaxTurnSeq is the highest turn_seq observed (heartbeats or acks).
func (t *TurnTracker) MaxTurnSeq() uint64 {
	if t == nil {
		return 0
	}
	return t.maxTurnSeq
}

// TurnCount is the number of turn records currently retained.
func (t *TurnTracker) TurnCount() int {
	if t == nil {
		return 0
	}
	return len(t.turns)
}

// HeartbeatAtCount is the number of nonce→turn_seq mappings currently retained.
func (t *TurnTracker) HeartbeatAtCount() int {
	if t == nil {
		return 0
	}
	return len(t.heartbeatAt)
}

// Clone returns a deep copy so trial-apply / independent verifiers do not share maps.
func (t *TurnTracker) Clone() *TurnTracker {
	if t == nil {
		return nil
	}
	cp := &TurnTracker{
		slotsNum:        t.slotsNum,
		quorum:          t.quorum,
		ackDeadline:     t.ackDeadline,
		retain:          t.retain,
		turns:           make(map[uint64]*SyncTurnRecord, len(t.turns)),
		heartbeatAt:     make(map[uint64]uint64, len(t.heartbeatAt)),
		stampHeight:     t.stampHeight,
		maxTurnSeq:      t.maxTurnSeq,
		lastCompleted:   t.lastCompleted,
		lastCompleteSeq: t.lastCompleteSeq,
	}
	for k, v := range t.turns {
		cp.turns[k] = cloneTurn(v)
	}
	for k, v := range t.heartbeatAt {
		cp.heartbeatAt[k] = v
	}
	return cp
}

// SeedCompleted restores h_last and the latest turn_seq from a snapshot when
// the journal cannot be replayed. Observe still ratchets both forward.
func (t *TurnTracker) SeedCompleted(lastCompleted, maxTurnSeq uint64) {
	if t == nil {
		return
	}
	if lastCompleted > t.lastCompleted {
		t.lastCompleted = lastCompleted
	}
	if maxTurnSeq > t.maxTurnSeq {
		t.maxTurnSeq = maxTurnSeq
		t.lastCompleteSeq = maxTurnSeq
	}
}

// LastCompletedHeight is h_last: mainnet height at which the last complete turn finished.
func (t *TurnTracker) LastCompletedHeight() uint64 {
	if t == nil {
		return 0
	}
	h := t.lastCompleted
	if t.stampHeight > h {
		return t.stampHeight
	}
	return h
}

// MissingAcks returns slots with no ack for turnSeq. The repair trigger is
// MissingAcksDue, which also requires the ack window to have closed.
func (t *TurnTracker) MissingAcks(turnSeq uint64) []uint32 {
	if t == nil {
		return nil
	}
	rec := t.turns[turnSeq]
	if rec == nil {
		return nil
	}
	missing := make([]uint32, 0)
	for slot := uint32(0); slot < uint32(t.slotsNum); slot++ {
		if _, ok := rec.Acks[slot]; !ok {
			missing = append(missing, slot)
		}
	}
	return missing
}

// MissingAcksDue is MissingAcks gated on the ack window having closed
// (hNow > h_req + D_ack, or the turn already degraded from a log-resident
// stamp). Repair probes use this, not MissingAcks. hNow must itself be
// log-derived (h_last / Diff stamps), never a live oracle tip.
func (t *TurnTracker) MissingAcksDue(turnSeq, hNow uint64) []uint32 {
	if t == nil {
		return nil
	}
	rec := t.turns[turnSeq]
	if rec == nil || rec.HReq == 0 {
		return nil
	}
	if rec.State != TurnDegraded && !t.windowClosed(rec, hNow) {
		return nil
	}
	return t.MissingAcks(turnSeq)
}

// RepairDue is one retained turn whose ack window has closed with missing slots.
type RepairDue struct {
	TurnSeq   uint64
	SpanStart uint64
	Missing   []uint32
}

// RepairDueAll lists every retained turn that is past D_ack with missing acks,
// newest last. Spec §11.3 probes turn s, not only Latest().
func (t *TurnTracker) RepairDueAll() []RepairDue {
	if t == nil {
		return nil
	}
	hNow := t.LastCompletedHeight()
	seqs := make([]uint64, 0, len(t.turns))
	for seq := range t.turns {
		seqs = append(seqs, seq)
	}
	slices.Sort(seqs)
	var out []RepairDue
	for _, seq := range seqs {
		rec := t.turns[seq]
		missing := t.MissingAcksDue(seq, hNow)
		if len(missing) == 0 {
			continue
		}
		spanStart := uint64(0)
		if rec != nil {
			spanStart = rec.RequestSpan[0]
		}
		out = append(out, RepairDue{TurnSeq: seq, SpanStart: spanStart, Missing: missing})
	}
	return out
}

// HeartbeatNonceForSlot is the nonce in [spanStart, spanStart+slotsNum) that
// addresses slot (executor(n) = n mod slots_num).
func HeartbeatNonceForSlot(spanStart uint64, slot, slotsNum uint32) uint64 {
	if slotsNum == 0 {
		return spanStart
	}
	n := uint64(slotsNum)
	for i := uint64(0); i < n; i++ {
		nonce := spanStart + i
		if SlotForNonce(nonce, n) == slot {
			return nonce
		}
	}
	return spanStart
}

// CompletedAtOrAbove reports whether some turn completed carrying height ≥ h.
//
// This is bookkeeping for operators, not a confirmation predicate: it was once
// (C-turn), which is withdrawn (see ConfirmationRule.RuleTurn). Q acks at ≥ h
// can all be one originator's claim lifted from the floor, so this says a turn
// closed while that height was in the air — never that h happened.
func (t *TurnTracker) CompletedAtOrAbove(h uint64) bool {
	if t == nil || h == 0 {
		return false
	}
	for _, rec := range t.turns {
		if rec.State != TurnComplete {
			continue
		}
		n := 0
		for _, a := range rec.Acks {
			if a.Height >= h {
				n++
			}
		}
		if n >= t.quorum {
			return true
		}
	}
	return false
}

func cloneTurn(rec *SyncTurnRecord) *SyncTurnRecord {
	cp := *rec
	cp.Acks = make(map[uint32]AckRecord, len(rec.Acks))
	for k, v := range rec.Acks {
		v.Hash = append([]byte(nil), v.Hash...)
		cp.Acks[k] = v
	}
	return &cp
}
