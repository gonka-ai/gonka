package heightsync

import (
	"sync"
	"time"

	"devshard/types"
)

// HeartbeatReason is MsgHeartbeat.reason (spec §10.4).
type HeartbeatReason string

const (
	ReasonHeightCadence HeartbeatReason = "height_cadence"
	ReasonQuietSession  HeartbeatReason = "quiet_session"
	ReasonForced        HeartbeatReason = "forced"
	ReasonCPoCBand      HeartbeatReason = "cpoc_band"
	ReasonNoHeight      HeartbeatReason = "no_height"
	// ReasonTurnTimeout reopens a turn that sat past TurnTimeout without
	// reaching quorum, so one unreachable slot cannot stall the cadence forever.
	ReasonTurnTimeout HeartbeatReason = "turn_timeout"
)

// Heartbeat is the producer's liveness scheduler. It answers one question: has
// a full height-sync turnover landed within Interval? A turnover is Q distinct
// host-signed height claims — heartbeat acks, or an executor stamp riding an
// ordinary Anchor round-trip. Either discharges the obligation, so a busy
// session emits no heartbeats and a quiet one emits exactly as many as it needs.
//
// Every field here is wall clock and producer-local. None of it reaches Diff:
// SyncTurnRecord stays a pure function of the log, evaluated on logged heights,
// so independent verifiers never have to agree on a clock.
type Heartbeat struct {
	mu sync.Mutex

	cfg    HeartbeatConfig
	quorum int

	skippedNoHeight int

	claimed      map[uint32]struct{} // distinct slots that claimed since the window opened
	lastTurnover time.Time
	turnOpenedAt time.Time
	turnOpen     bool
	turnovers    int
	abandoned    int
}

// NewHeartbeat constructs a scheduler. Zero config fields take compiled
// defaults. Call SetRoster to supply the quorum a turnover needs.
func NewHeartbeat(cfg HeartbeatConfig) *Heartbeat {
	return &Heartbeat{
		cfg:     cfg.withDefaults(),
		quorum:  1,
		claimed: make(map[uint32]struct{}),
	}
}

// SetRoster sets the turnover quorum. quorum <= 0 uses QuorumForRoster(slotsNum),
// the same Q as (C-quorum) and (C-turn).
func (h *Heartbeat) SetRoster(slotsNum uint64, quorum int) {
	if h == nil {
		return
	}
	if slotsNum == 0 {
		slotsNum = 1
	}
	if quorum <= 0 {
		quorum = QuorumForRoster(int(slotsNum))
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.quorum = quorum
}

func (h *Heartbeat) Config() HeartbeatConfig {
	if h == nil {
		return DefaultHeartbeatConfig()
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.cfg
}

// SkippedNoHeight is the H3 skip counter (ObservedHeightNow empty).
func (h *Heartbeat) SkippedNoHeight() int {
	if h == nil {
		return 0
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.skippedNoHeight
}

// Turnovers counts completed height-sync round-trips (heartbeat or stamped).
func (h *Heartbeat) Turnovers() int {
	if h == nil {
		return 0
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.turnovers
}

// AbandonedTurns counts turns reopened after TurnTimeout without quorum.
func (h *Heartbeat) AbandonedTurns() int {
	if h == nil {
		return 0
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.abandoned
}

// SinceTurnover is how long the session has gone without a full turnover.
// Zero when none has happened yet, which reads as "due now".
func (h *Heartbeat) SinceTurnover(now time.Time) time.Duration {
	if h == nil {
		return 0
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.lastTurnover.IsZero() {
		return 0
	}
	return now.Sub(h.lastTurnover)
}

// Due reports whether the producer must open a heartbeat turn now. hNow is the
// height it would stamp: zero means it holds no claim and must not invent one
// (H3). An open turn suppresses new ones until TurnTimeout expires.
func (h *Heartbeat) Due(now time.Time, hNow uint64) (bool, HeartbeatReason) {
	if h == nil {
		h = NewHeartbeat(DefaultHeartbeatConfig())
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if hNow == 0 {
		h.skippedNoHeight++
		return false, ReasonNoHeight
	}
	if h.turnOpen {
		if now.Sub(h.turnOpenedAt) < h.cfg.TurnTimeout {
			return false, ""
		}
		return true, ReasonTurnTimeout
	}
	if h.lastTurnover.IsZero() || now.Sub(h.lastTurnover) >= h.cfg.Interval {
		return true, ReasonQuietSession
	}
	return false, ""
}

// Deadline is the instant by which a due turn must have turned over before a
// host's idle budget starts running: lastTurnover + Interval + TurnTimeout.
func (h *Heartbeat) Deadline(now time.Time) time.Time {
	if h == nil {
		h = NewHeartbeat(DefaultHeartbeatConfig())
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	from := h.lastTurnover
	if from.IsZero() {
		from = now
	}
	return from.Add(h.cfg.TurnoverBudget())
}

// OpenTurn records a dispatched heartbeat span. A previous turn that never
// reached quorum is abandoned here: its SyncTurnRecord still degrades from the
// log on logged heights, but the producer stops waiting on it.
func (h *Heartbeat) OpenTurn(at time.Time) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.turnOpen {
		h.abandoned++
	}
	h.turnOpen = true
	h.turnOpenedAt = at
	h.claimed = make(map[uint32]struct{})
}

// SettleTurn drops the open-turn suppression because the log has already
// decided the turn. A degraded record leaves nothing left to wait for, so the
// producer need not burn the rest of TurnTimeout before trying again. This is
// not a turnover: only Q claims discharge the Interval budget.
func (h *Heartbeat) SettleTurn() {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.turnOpen = false
	h.turnOpenedAt = time.Time{}
	h.claimed = make(map[uint32]struct{})
}

// NoteClaim records a host-signed height claim from slot: a MsgHeightAck, or an
// executor-stamped response. A user-signed stamp is the producer's own claim
// and must never be passed here — it proves no round-trip. Reaching Q distinct
// slots is a full turnover and restarts the Interval budget.
func (h *Heartbeat) NoteClaim(slot uint32, at time.Time) (turnover bool) {
	if h == nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.claimed == nil {
		h.claimed = make(map[uint32]struct{})
	}
	h.claimed[slot] = struct{}{}
	if len(h.claimed) < h.quorum {
		return false
	}
	h.lastTurnover = at
	h.turnOpen = false
	h.turnOpenedAt = time.Time{}
	h.turnovers++
	h.claimed = make(map[uint32]struct{})
	return true
}

// SlotForNonce is executor(n) = n mod slots_num. Any consecutive span of
// slots_num nonces addresses every slot exactly once.
func SlotForNonce(nonce, slotsNum uint64) uint32 {
	if slotsNum == 0 {
		return 0
	}
	return uint32(nonce % slotsNum)
}

// SpanTxs returns slots_num MsgHeartbeat txs for one turn, with no ack wait.
// hNow == 0 yields nil (H3: do not claim a height).
func (h *Heartbeat) SpanTxs(turnSeq, hNow uint64, hash []byte, slotsNum uint64, reason HeartbeatReason, prevVector []*types.SyncVectorEntry) []*types.DevshardTx {
	if h == nil {
		h = NewHeartbeat(DefaultHeartbeatConfig())
	}
	if hNow == 0 {
		h.mu.Lock()
		h.skippedNoHeight++
		h.mu.Unlock()
		return nil
	}
	if slotsNum == 0 {
		slotsNum = 1
	}
	if reason == "" {
		reason = ReasonQuietSession
	}
	out := make([]*types.DevshardTx, 0, slotsNum)
	for i := uint64(0); i < slotsNum; i++ {
		hb := &types.MsgHeartbeat{
			TurnSeq:           turnSeq,
			ObservedHeight:    hNow,
			ObservedBlockHash: append([]byte(nil), hash...),
			SlotsNum:          slotsNum,
			Reason:            string(reason),
			SyncVector:        prevVector,
		}
		out = append(out, &types.DevshardTx{Tx: &types.DevshardTx_Heartbeat{Heartbeat: hb}})
	}
	return out
}

// SpanNonces returns the consecutive nonce span [start, start+slotsNum).
func SpanNonces(startNonce, slotsNum uint64) []uint64 {
	if slotsNum == 0 {
		slotsNum = 1
	}
	out := make([]uint64, slotsNum)
	for i := uint64(0); i < slotsNum; i++ {
		out[i] = startNonce + i
	}
	return out
}
