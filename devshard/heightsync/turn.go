package heightsync

import (
	"maps"
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
//
// TurnStart is the turn's identity: the nonce of the first heartbeat in its
// span. Nothing on the wire names a turn — the sequencer used to pick a
// turn_seq, which let it name a turn 2^60 ahead and prune the retain window.
// The span-start nonce is assigned by the log instead, and is monotone and
// bounded for free.
type SyncTurnRecord struct {
	TurnStart         uint64
	RequestSpan       [2]uint64 // [TurnStart, TurnStart+slots_num-1]
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

// TurnTracker folds heartbeat + ack txs into SyncTurnRecords. Q is the turn
// reachability threshold (ceil(2/3 × slots)); it is not a height certificate.
type TurnTracker struct {
	slotsNum          uint64
	quorum            int
	ackDeadline       uint64
	retain            uint64
	turns             map[uint64]*SyncTurnRecord
	heartbeatAt       map[uint64]uint64 // heartbeat nonce → turn start (L3)
	stampHeight       uint64            // max host-signed confirm/finish stamp; discharges h_last
	latestTurnStart   uint64
	lastCompleted     uint64 // max CompletedAtHeight; survives prune
	lastCompleteStart uint64
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

// ArmingContext returns the newest complete turn's start nonce and the start
// nonces of degraded turns.
func (t *TurnTracker) ArmingContext() (lastComplete uint64, degraded []uint64) {
	if t == nil {
		return 0, nil
	}
	for start, rec := range t.turns {
		if rec == nil {
			continue
		}
		if rec.State == TurnDegraded {
			degraded = append(degraded, start)
		}
	}
	slices.Sort(degraded)
	return t.lastCompleteStart, degraded
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
		if h, ok := executorInferenceStamp(tx); ok && h > t.stampHeight {
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

// executorInferenceStamp is a host-signed inference stamp that may discharge
// h_last on a busy escrow. MsgStartInference is user-signed and is not a
// round-trip, so it must not ratchet the turn clock (same split as F).
func executorInferenceStamp(tx *types.DevshardTx) (uint64, bool) {
	if conf := tx.GetConfirmStart(); conf != nil {
		return inferenceStamp(conf)
	}
	if fin := tx.GetFinishInference(); fin != nil {
		return inferenceStamp(fin)
	}
	return 0, false
}

// turnStartFor names the turn a heartbeat at nonce belongs to.
//
// A span is slots_num consecutive nonces, and applyCore only admits
// LatestNonce+1, so the chain has no gaps: a span's heartbeats always land as a
// prefix beginning at the span start. A heartbeat inside the newest turn's span
// therefore joins it, and anything past that span opens a turn of its own. No
// wire field is consulted, so there is nothing for the sequencer to inflate.
func (t *TurnTracker) turnStartFor(nonce uint64) uint64 {
	rec := t.turns[t.latestTurnStart]
	if rec != nil && nonce >= rec.RequestSpan[0] && nonce <= rec.RequestSpan[1] {
		return rec.RequestSpan[0]
	}
	return nonce
}

func (t *TurnTracker) observeHeartbeat(nonce uint64, hb *types.MsgHeartbeat) {
	if t.heartbeatAt == nil {
		t.heartbeatAt = make(map[uint64]uint64)
	}
	start := t.turnStartFor(nonce)
	t.heartbeatAt[nonce] = start
	if start > t.latestTurnStart {
		t.latestTurnStart = start
	}
	rec := t.turns[start]
	if rec == nil {
		slots := hb.SlotsNum
		if slots == 0 {
			slots = t.slotsNum
		}
		rec = &SyncTurnRecord{
			TurnStart:   start,
			RequestSpan: [2]uint64{start, start + slots - 1},
			HReq:        heartbeatRequestHeight(hb),
			Acks:        make(map[uint32]AckRecord),
			State:       TurnOpen,
			Reason:      hb.Reason,
		}
		t.turns[start] = rec
		SetTurnState("open")
		return
	}
	if rec.Reason == "" && hb.Reason != "" {
		rec.Reason = hb.Reason
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

// observeAck folds a host ack into the turn its ref_nonce names.
//
// An ack no longer carries a turn id, so there is no id to disagree with the
// log: the turn is heartbeatAt[ref_nonce]. L3 rejects an ack whose ref_nonce has
// no heartbeat, so an unresolved ref_nonce here means the turn record was
// pruned. That is dropped rather than resurrected — the old code minted a fresh
// record at the ack's claimed seq, which prune then removed again.
func (t *TurnTracker) observeAck(nonce uint64, ack *types.MsgHeightAck) {
	start, ok := t.heartbeatAt[ack.RefNonce]
	if !ok {
		return
	}
	rec := t.turns[start]
	if rec == nil {
		return
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

// prune keeps the newest DefaultTurnRetain turns. Turn ids are span-start
// nonces, so "newest" is by id and the retained set is the tail of the sorted
// keys. A cutoff arithmetic on the id would be wrong here: nonces are not one
// apart per turn the way turn_seq was.
func (t *TurnTracker) prune() {
	if t.retain == 0 {
		t.retain = DefaultTurnRetain
	}
	if uint64(len(t.turns)) <= t.retain {
		return
	}
	starts := make([]uint64, 0, len(t.turns))
	for start := range t.turns {
		starts = append(starts, start)
	}
	slices.Sort(starts)
	for _, start := range starts[:uint64(len(starts))-t.retain] {
		delete(t.turns, start)
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
// sync_state used to gate this when turn completion was mistaken for a height
// certificate. Completion now certifies only that Q slots were reachable and
// applying the log, which an ORACLE_UNAVAILABLE slot proves as well as a SYNCED
// one — it echoes F(m) from the log it already has. Excluding it instead made
// an honest host with a dead follower a permanent hole in the roster's cadence.
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
		if rec.TurnStart > t.lastCompleteStart {
			t.lastCompleteStart = rec.TurnStart
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

// Record returns a copy of the turn starting at turnStart, or nil.
func (t *TurnTracker) Record(turnStart uint64) *SyncTurnRecord {
	if t == nil {
		return nil
	}
	rec := t.turns[turnStart]
	if rec == nil {
		return nil
	}
	return cloneTurn(rec)
}

// Latest is the newest turn observed.
func (t *TurnTracker) Latest() *SyncTurnRecord {
	if t == nil || t.latestTurnStart == 0 {
		return nil
	}
	rec := t.turns[t.latestTurnStart]
	if rec == nil {
		return nil
	}
	return cloneTurn(rec)
}

// HeartbeatTurn reports the turn start of the MsgHeartbeat at nonce, if any.
func (t *TurnTracker) HeartbeatTurn(nonce uint64) (uint64, bool) {
	if t == nil {
		return 0, false
	}
	start, ok := t.heartbeatAt[nonce]
	return start, ok
}

// TurnStartFor names the turn a heartbeat landing at nonce would belong to,
// against what the tracker has already folded. The log plane needs this before
// Observe runs, so the rule lives in one place.
func (t *TurnTracker) TurnStartFor(nonce uint64) uint64 {
	if t == nil {
		return nonce
	}
	return t.turnStartFor(nonce)
}

// TurnBefore is the newest retained turn starting before start, or 0.
// Turn ids are nonces, so a predecessor is a search rather than start-1.
func (t *TurnTracker) TurnBefore(start uint64) uint64 {
	if t == nil {
		return 0
	}
	var prev uint64
	for s := range t.turns {
		if s < start && s > prev {
			prev = s
		}
	}
	return prev
}

// LatestTurnStart is the span-start nonce of the newest turn observed.
func (t *TurnTracker) LatestTurnStart() uint64 {
	if t == nil {
		return 0
	}
	return t.latestTurnStart
}

// TurnCount is the number of turn records currently retained.
func (t *TurnTracker) TurnCount() int {
	if t == nil {
		return 0
	}
	return len(t.turns)
}

// HeartbeatAtCount is the number of nonce→turn-start mappings currently retained.
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
		slotsNum:          t.slotsNum,
		quorum:            t.quorum,
		ackDeadline:       t.ackDeadline,
		retain:            t.retain,
		turns:             make(map[uint64]*SyncTurnRecord, len(t.turns)),
		heartbeatAt:       make(map[uint64]uint64, len(t.heartbeatAt)),
		stampHeight:       t.stampHeight,
		latestTurnStart:   t.latestTurnStart,
		lastCompleted:     t.lastCompleted,
		lastCompleteStart: t.lastCompleteStart,
	}
	for k, v := range t.turns {
		cp.turns[k] = cloneTurn(v)
	}
	maps.Copy(cp.heartbeatAt, t.heartbeatAt)
	return cp
}

// SeedCompleted restores h_last and the latest turn start from a snapshot when
// the journal cannot be replayed. Observe still ratchets both forward.
func (t *TurnTracker) SeedCompleted(lastCompleted, latestTurnStart uint64) {
	if t == nil {
		return
	}
	if lastCompleted > t.lastCompleted {
		t.lastCompleted = lastCompleted
	}
	if latestTurnStart > t.latestTurnStart {
		t.latestTurnStart = latestTurnStart
		t.lastCompleteStart = latestTurnStart
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

// MissingAcks returns slots with no ack for the turn at turnStart. The repair
// trigger is MissingAcksDue, which also requires the ack window to have closed.
func (t *TurnTracker) MissingAcks(turnStart uint64) []uint32 {
	if t == nil {
		return nil
	}
	rec := t.turns[turnStart]
	if rec == nil {
		return nil
	}
	missing := make([]uint32, 0)
	for slot := range uint32(t.slotsNum) {
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
func (t *TurnTracker) MissingAcksDue(turnStart, hNow uint64) []uint32 {
	if t == nil {
		return nil
	}
	rec := t.turns[turnStart]
	if rec == nil || rec.HReq == 0 {
		return nil
	}
	if rec.State != TurnDegraded && !t.windowClosed(rec, hNow) {
		return nil
	}
	return t.MissingAcks(turnStart)
}

// RepairDue is one retained turn whose ack window has closed with missing slots.
// TurnStart is both the turn's identity and its span start; they were separate
// fields only while a turn had a wire-assigned id.
type RepairDue struct {
	TurnStart uint64
	Missing   []uint32
}

// RepairDueAll lists every retained turn that is past D_ack with missing acks,
// newest last. Spec §11.3 probes turn s, not only Latest().
func (t *TurnTracker) RepairDueAll() []RepairDue {
	if t == nil {
		return nil
	}
	hNow := t.LastCompletedHeight()
	starts := make([]uint64, 0, len(t.turns))
	for start := range t.turns {
		starts = append(starts, start)
	}
	slices.Sort(starts)
	var out []RepairDue
	for _, start := range starts {
		missing := t.MissingAcksDue(start, hNow)
		if len(missing) == 0 {
			continue
		}
		out = append(out, RepairDue{TurnStart: start, Missing: missing})
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
	for i := range n {
		nonce := spanStart + i
		if SlotForNonce(nonce, n) == slot {
			return nonce
		}
	}
	return spanStart
}

// CompletedAtOrAbove reports whether some turn completed carrying height ≥ h.
//
// This is bookkeeping for operators, not a confirmation predicate. Q acks at
// ≥ h can all be one originator's claim lifted from the floor, so this says a
// turn closed while that height was in the air — never that h happened.
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
