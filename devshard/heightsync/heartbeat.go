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

	lastTurnoverFromStamp bool
	lastLimited           map[CadenceEventKind]time.Time
	ring                  cadenceRing
	// cadenceTotals counts every due-check disposition, including the ones the
	// ring rate-limits. cadence_events_total is derived from this, so the
	// discharged-by-inference ratio reports real savings rather than one
	// sample per Interval.
	cadenceTotals      map[CadenceEventKind]uint64
	skippedRealTraffic int
}

// NewHeartbeat constructs a scheduler. Zero config fields take compiled
// defaults. Call SetRoster to supply the quorum a turnover needs.
func NewHeartbeat(cfg HeartbeatConfig) *Heartbeat {
	return &Heartbeat{
		cfg:           cfg.withDefaults(),
		quorum:        1,
		claimed:       make(map[uint32]struct{}),
		ring:          newCadenceRing(DefaultCadenceRingCapacity),
		cadenceTotals: make(map[CadenceEventKind]uint64),
	}
}

// SetRoster sets the turnover quorum. quorum <= 0 uses QuorumForRoster(slotsNum).
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

// SkippedNoHeight counts Due() calls that skipped because ObservedHeightNow
// was empty (spec §10.3).
func (h *Heartbeat) SkippedNoHeight() int {
	if h == nil {
		return 0
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.skippedNoHeight
}

// SkippedRealTraffic counts due-checks that found a stamp turnover already
// inside Interval (inference discharged the heartbeat). Every qualifying
// check increments this; the cadence ring is rate-limited separately.
func (h *Heartbeat) SkippedRealTraffic() int {
	if h == nil {
		return 0
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.skippedRealTraffic
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
// (spec §10.3). An open turn suppresses new ones until TurnTimeout expires.
func (h *Heartbeat) Due(now time.Time, hNow uint64) (bool, HeartbeatReason) {
	if h == nil {
		h = NewHeartbeat(DefaultHeartbeatConfig())
	}
	h.mu.Lock()
	due, reason, logEv := h.dueLocked(now, hNow)
	h.mu.Unlock()
	if reason == ReasonNoHeight {
		IncHeartbeatSkipped("no_height")
	}
	if logEv != nil {
		logCadence(*logEv)
	}
	return due, reason
}

func (h *Heartbeat) dueLocked(now time.Time, hNow uint64) (bool, HeartbeatReason, *CadenceEvent) {
	if hNow == 0 {
		h.skippedNoHeight++
		ev, sampled := h.recordCadenceLocked(CadenceEvent{
			At:     now,
			Event:  CadenceSkippedNoHeight,
			Reason: string(ReasonNoHeight),
			Quorum: h.quorum,
		})
		if !sampled {
			return false, ReasonNoHeight, nil
		}
		return false, ReasonNoHeight, &ev
	}
	if h.turnOpen {
		if now.Sub(h.turnOpenedAt) < h.cfg.TurnTimeout {
			return false, "", nil
		}
		return true, ReasonTurnTimeout, nil
	}
	if h.lastTurnover.IsZero() || now.Sub(h.lastTurnover) >= h.cfg.Interval {
		return true, ReasonQuietSession, nil
	}
	return false, "", nil
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
// log on logged heights, but the producer stops waiting on it. The bool is
// whether the previous open turn was abandoned.
func (h *Heartbeat) OpenTurn(at time.Time) (abandoned bool) {
	if h == nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.turnOpen {
		h.abandoned++
		abandoned = true
	}
	h.turnOpen = true
	h.turnOpenedAt = at
	h.claimed = make(map[uint32]struct{})
	return abandoned
}

// TurnOpen reports whether the producer is waiting on an in-flight span.
func (h *Heartbeat) TurnOpen() bool {
	if h == nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.turnOpen
}

// LastTurnoverFromStamp reports whether the last Q-claim turnover was from
// executor stamps rather than heartbeat acks.
func (h *Heartbeat) LastTurnoverFromStamp() bool {
	if h == nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.lastTurnoverFromStamp
}

// Quorum is the turnover Q this scheduler was configured with.
func (h *Heartbeat) Quorum() int {
	if h == nil {
		return 0
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.quorum
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
	return h.noteClaim(slot, at, false)
}

// NoteStamp is NoteClaim for an executor-stamped receipt. A turnover from
// stamps is what discharges the heartbeat obligation on a busy session.
func (h *Heartbeat) NoteStamp(slot uint32, at time.Time) (turnover bool) {
	return h.noteClaim(slot, at, true)
}

func (h *Heartbeat) noteClaim(slot uint32, at time.Time, stamp bool) (turnover bool) {
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
	h.lastTurnoverFromStamp = stamp
	h.claimed = make(map[uint32]struct{})
	return true
}

// RecordCadence counts one due-check disposition and, unless the kind is
// rate-limited and already sampled this Interval, appends it to the last-N ring
// and emits `heightsync: cadence` for citest / docker logs.
func (h *Heartbeat) RecordCadence(ev CadenceEvent) {
	if h == nil {
		return
	}
	h.mu.Lock()
	ev, sampled := h.recordCadenceLocked(ev)
	h.mu.Unlock()
	if sampled {
		logCadence(ev)
	}
}

func cadenceKindLimited(kind CadenceEventKind) bool {
	switch kind {
	case CadenceSkippedNoHeight, CadenceDischargedByInference:
		return true
	default:
		return false
	}
}

func (h *Heartbeat) allowLimitedLocked(kind CadenceEventKind, now time.Time) bool {
	if !cadenceKindLimited(kind) {
		return true
	}
	last := h.lastLimited[kind]
	if !last.IsZero() && now.Sub(last) < h.cfg.Interval {
		return false
	}
	return true
}

// recordCadenceLocked is the one place a cadence disposition is booked. The
// total always moves; sampled reports whether the event also went into the ring
// and the log, which the two rate-limited kinds do at most once per Interval.
func (h *Heartbeat) recordCadenceLocked(ev CadenceEvent) (CadenceEvent, bool) {
	if ev.Quorum == 0 {
		ev.Quorum = h.quorum
	}
	if h.cadenceTotals == nil {
		h.cadenceTotals = make(map[CadenceEventKind]uint64)
	}
	h.cadenceTotals[ev.Event]++
	if !h.allowLimitedLocked(ev.Event, ev.At) {
		return ev, false
	}
	if h.ring.buf == nil {
		h.ring = newCadenceRing(DefaultCadenceRingCapacity)
	}
	h.ring.append(ev)
	if cadenceKindLimited(ev.Event) {
		if h.lastLimited == nil {
			h.lastLimited = make(map[CadenceEventKind]time.Time)
		}
		h.lastLimited[ev.Event] = ev.At
	}
	return ev, true
}

// MaybeRecordDischarged records discharged_by_inference after a stamp turnover
// suppressed a due heartbeat. The Prometheus skip counter increments on every
// qualifying due-check; the ring and log line are at most once per Interval.
// An in-flight turn is not a discharge.
func (h *Heartbeat) MaybeRecordDischarged(now time.Time, hRef uint64) bool {
	if h == nil {
		return false
	}
	h.mu.Lock()
	if h.turnOpen || !h.lastTurnoverFromStamp {
		h.mu.Unlock()
		return false
	}
	h.skippedRealTraffic++
	ev, sampled := h.recordCadenceLocked(CadenceEvent{
		At:     now,
		Event:  CadenceDischargedByInference,
		HRef:   hRef,
		Quorum: h.quorum,
	})
	h.mu.Unlock()
	IncHeartbeatSkipped("real_traffic")
	if !sampled {
		return false
	}
	logCadence(ev)
	return true
}

// CadenceSnapshot copies the last-N ring and the per-occurrence event totals.
// The ring samples two kinds at most once per Interval; the totals do not.
func (h *Heartbeat) CadenceSnapshot() (events []CadenceEvent, counts map[string]uint64) {
	if h == nil {
		return nil, map[string]uint64{}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	events, _ = h.ring.snapshot()
	return events, copyCadenceCounts(h.cadenceTotals)
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
// hNow == 0 yields nil (spec §10.3: do not claim a height).
//
// The turn is not named here. Its identity is the nonce the first tx of this
// span lands at, which the producer does not get to pick.
func (h *Heartbeat) SpanTxs(hNow uint64, hash []byte, slotsNum uint64, reason HeartbeatReason, prevVector []*types.SyncVectorEntry) []*types.DevshardTx {
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
	for range slotsNum {
		hb := &types.MsgHeartbeat{
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
	for i := range slotsNum {
		out[i] = startNonce + i
	}
	return out
}
