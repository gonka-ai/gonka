package heightsync

import "devshard/types"

// HeartbeatReason is MsgHeartbeat.reason (spec §10.4).
type HeartbeatReason string

const (
	ReasonHeightCadence HeartbeatReason = "height_cadence"
	ReasonQuietSession  HeartbeatReason = "quiet_session"
	ReasonForced        HeartbeatReason = "forced"
	ReasonCPoCBand      HeartbeatReason = "cpoc_band"
	ReasonNoHeight      HeartbeatReason = "no_height"
)

// Heartbeat is the pure obligation calculator (no session wiring).
type Heartbeat struct {
	cfg             HeartbeatConfig
	skippedNoHeight int
}

// NewHeartbeat constructs an obligation calculator. Zero fields take compiled defaults.
func NewHeartbeat(cfg HeartbeatConfig) *Heartbeat {
	return &Heartbeat{cfg: cfg.withDefaults()}
}

func (h *Heartbeat) Config() HeartbeatConfig {
	if h == nil {
		return DefaultHeartbeatConfig()
	}
	return h.cfg
}

// SkippedNoHeight is the H3 skip counter (ObservedHeightNow empty).
func (h *Heartbeat) SkippedNoHeight() int {
	if h == nil {
		return 0
	}
	return h.skippedNoHeight
}

// Due reports whether a quiet session must open a heartbeat turn.
// Due when hNow − hLast ≥ K_hb (K_hb=1 ⇒ every new block). Open by Deadline.
func (h *Heartbeat) Due(hNow, hLast uint64) (bool, HeartbeatReason) {
	if h == nil {
		h = NewHeartbeat(DefaultHeartbeatConfig())
	}
	if hNow == 0 {
		h.skippedNoHeight++
		return false, ReasonNoHeight
	}
	if hNow <= hLast {
		return false, ""
	}
	if hNow-hLast >= h.cfg.IntervalBlocks {
		return true, ReasonQuietSession
	}
	return false, ""
}

// Deadline is hLast + K_hb + D_ack: the last height at which a due turn may still open.
func (h *Heartbeat) Deadline(hLast uint64) uint64 {
	if h == nil {
		h = NewHeartbeat(DefaultHeartbeatConfig())
	}
	return hLast + h.cfg.IntervalBlocks + h.cfg.AckDeadlineBlocks
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
		h.skippedNoHeight++
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
