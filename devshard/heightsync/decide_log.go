package heightsync

import (
	"devshard/logging"
)

// Decide log events (grep: heightsync: decide).
const (
	DecideEventOmitNotDue     = "decide_omit_not_due"
	DecideEventOmitNoSource   = "decide_omit_no_source"
	DecideEventOmitStale      = "decide_omit_stale"
	DecideEventAnchorStale    = "decide_anchor_stale"
	DecideEventOmitLatestErr  = "decide_omit_latest_error"
	DecideEventOmitNilSection = "decide_omit_nil_section"
	DecideEventAnchor         = "decide_anchor"
)

// logDecide emits a structured debug line for AnchorScheduler.Decide outcomes.
func logDecide(h DecideHints, schedK, schedSlots uint64, cadenceEmit, lazyEmit, forceOracle, syncTurn, oracleMiss bool, event string, snap OracleDecideSnapshot, decideErr error) {
	if event == DecideEventOmitNotDue {
		return
	}
	kvs := []any{
		LogFieldSubsystem, "heightsync",
		LogFieldEvent, event,
		LogFieldNonce, h.Nonce,
		"cadence_emit", cadenceEmit,
		"lazy_emit", lazyEmit,
		"sync_turn", syncTurn,
		"force_oracle", forceOracle,
		"oracle_miss", oracleMiss,
		"oracle_source", snap.SourceKind,
		"oracle_stale", snap.Stale,
	}
	if h.Direction != "" {
		kvs = append(kvs, LogFieldDirection, h.Direction)
	}
	if h.SessionStart {
		kvs = append(kvs, "session_start", true)
	}
	if h.ForceAnchor {
		kvs = append(kvs, "force_anchor_hint", true)
	}
	if h.Recipient != "" {
		kvs = append(kvs, "recipient", h.Recipient)
	}
	if h.Escrow != nil && h.Escrow.ForcedEnd != 0 {
		kvs = append(kvs, LogFieldForcedStart, h.Escrow.ForcedStart, LogFieldForcedEnd, h.Escrow.ForcedEnd)
	}
	if snap.SourceKind == "local_block_oracle" {
		if snap.NeverReceived {
			kvs = append(kvs, "oracle_never_received", true)
		} else if snap.LastRecvAgeMs >= 0 {
			kvs = append(kvs, "oracle_last_recv_age_ms", snap.LastRecvAgeMs)
		}
		if snap.LatestHeight > 0 {
			kvs = append(kvs, LogFieldHeight, snap.LatestHeight)
		}
	}
	if event == DecideEventAnchorStale && snap.LastRecvAgeMs > 0 {
		kvs = append(kvs, "tip_stale_after_ms", snap.LastRecvAgeMs)
	}
	if snap.SourceKind == "peer_tip_cache" {
		kvs = append(kvs, LogFieldCacheReady, snap.PeerTipCacheReady, LogFieldVerifiedOrigins, snap.VerifiedOrigins)
		if snap.LatestHeight > 0 {
			kvs = append(kvs, LogFieldHeight, snap.LatestHeight)
		}
	}
	if decideErr != nil {
		kvs = append(kvs, "error", decideErr.Error())
	}
	if schedK > 0 {
		kvs = append(kvs, "scheduler_k", schedK, "scheduler_slots", schedSlots)
	}
	logging.Debug("heightsync: decide", kvs...)
}
