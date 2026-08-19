package heightsync

import "devshard/types"

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
}

// TurnTracker folds heartbeat + ack txs into SyncTurnRecords. Q is the same
// knob as (C-quorum); there is no second quorum parameter.
type TurnTracker struct {
	slotsNum    uint64
	quorum      int
	ackDeadline uint64
	turns       map[uint64]*SyncTurnRecord
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
		turns:       make(map[uint64]*SyncTurnRecord),
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
	}
	for _, tx := range txs {
		if tx == nil {
			continue
		}
		if ack := tx.GetHeightAck(); ack != nil {
			t.observeAck(diffNonce, ack)
		}
	}
	t.AdvanceHeight(hNow)
}

func (t *TurnTracker) observeHeartbeat(nonce uint64, hb *types.MsgHeartbeat) {
	rec := t.turns[hb.TurnSeq]
	if rec == nil {
		slots := hb.SlotsNum
		if slots == 0 {
			slots = t.slotsNum
		}
		rec = &SyncTurnRecord{
			TurnSeq:     hb.TurnSeq,
			RequestSpan: [2]uint64{nonce, nonce + slots - 1},
			HReq:        hb.ObservedHeight,
			Acks:        make(map[uint32]AckRecord),
			State:       TurnOpen,
		}
		t.turns[hb.TurnSeq] = rec
		return
	}
	if nonce < rec.RequestSpan[0] {
		span := rec.RequestSpan[1] - rec.RequestSpan[0]
		rec.RequestSpan = [2]uint64{nonce, nonce + span}
	}
	if hb.ObservedHeight != 0 && (rec.HReq == 0 || hb.ObservedHeight < rec.HReq) {
		rec.HReq = hb.ObservedHeight
	}
}

func (t *TurnTracker) observeAck(nonce uint64, ack *types.MsgHeightAck) {
	rec := t.turns[ack.TurnSeq]
	if rec == nil {
		rec = &SyncTurnRecord{
			TurnSeq: ack.TurnSeq,
			Acks:    make(map[uint32]AckRecord),
			State:   TurnOpen,
		}
		t.turns[ack.TurnSeq] = rec
	}
	// Lateness is stamp-based: ingest height may tick during transport.
	late := rec.State == TurnDegraded
	if rec.HReq > 0 && ack.ObservedHeight > rec.HReq+t.ackDeadline {
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
}

// AdvanceHeight re-evaluates open turns against the current chain height.
func (t *TurnTracker) AdvanceHeight(hNow uint64) {
	if t == nil {
		return
	}
	for _, rec := range t.turns {
		t.recompute(rec, hNow)
	}
}

func (t *TurnTracker) windowClosed(rec *SyncTurnRecord, hNow uint64) bool {
	if rec.HReq == 0 {
		return false
	}
	deadline := rec.HReq + t.ackDeadline
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

func (t *TurnTracker) countingAcks(rec *SyncTurnRecord) int {
	n := 0
	for _, a := range rec.Acks {
		if a.Late {
			continue
		}
		if a.SyncState != types.SyncState_ORACLE_UNAVAILABLE {
			n++
		}
	}
	return n
}

func (t *TurnTracker) recompute(rec *SyncTurnRecord, hNow uint64) {
	if rec.State == TurnDegraded {
		return // late acks never un-degrade (attack 22)
	}
	counting := t.countingAcks(rec)
	if t.windowClosed(rec, hNow) && counting < t.quorum {
		rec.State = TurnDegraded
		return
	}
	if counting >= t.quorum {
		rec.State = TurnComplete
		rec.CompletedAtHeight = hNow
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
	if t == nil || len(t.turns) == 0 {
		return nil
	}
	var best uint64
	var rec *SyncTurnRecord
	for seq, r := range t.turns {
		if rec == nil || seq > best {
			best = seq
			rec = r
		}
	}
	return cloneTurn(rec)
}

// LastCompletedHeight is h_last: mainnet height at which the last complete turn finished.
func (t *TurnTracker) LastCompletedHeight() uint64 {
	if t == nil {
		return 0
	}
	var h uint64
	for _, rec := range t.turns {
		if rec.State == TurnComplete && rec.CompletedAtHeight > h {
			h = rec.CompletedAtHeight
		}
	}
	return h
}

// MissingAcks returns slots with no ack once height has passed h_req + D_ack.
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

// Confirms is (C-turn): a complete record exists with ≥ Q counting acks at height ≥ h.
func (t *TurnTracker) Confirms(h uint64) bool {
	if t == nil || h == 0 {
		return false
	}
	for _, rec := range t.turns {
		if rec.State != TurnComplete {
			continue
		}
		n := 0
		for _, a := range rec.Acks {
			if a.SyncState == types.SyncState_ORACLE_UNAVAILABLE {
				continue
			}
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
