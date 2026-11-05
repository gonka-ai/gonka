package state

import (
	"bytes"
	"encoding/hex"
	"fmt"

	"devshard/heightsync"
	"devshard/logging"
	"devshard/types"
)

// checkObservedStampTimeLocked applies the optimistic map rule on apply.
// On StampRequiresTime tags, a stamped message (height > 0) with
// observed_time_ms <= 0 is ErrStampTimeMissing (never ErrHeaderPending).
// Earlier tags still accept 0. A map miss autotrusts the carried time; a
// map hit that disagrees rejects. Never waits and never calls HeaderTimeMs.
// Caller holds sm.mu.
func (sm *StateMachine) checkObservedStampTimeLocked(leg string, height uint64, hash []byte, observedTimeMs int64) error {
	if height == 0 {
		return nil
	}
	if observedTimeMs <= 0 {
		if types.StampRequiresTime(sm.state.StateRootAndProtocolVersion) {
			return fmt.Errorf("%w: %s height %d", types.ErrStampTimeMissing, leg, height)
		}
		return nil
	}
	src, ok := sm.storedHeadersLocked()
	if !ok {
		return nil
	}
	_, err := heightsync.ResolveObservedTimeMs(src, height, hash, observedTimeMs)
	if err == nil {
		return nil
	}
	return fmt.Errorf("%w: %s height %d hash %s carried=%d", err, leg, height, hex.EncodeToString(hash), observedTimeMs)
}

// warnDeferredStampTimeMismatches compares carried clocks against the header
// map after a later poller fill. Warn only; state and floor are not mutated.
// Covers live start/confirm stamps and the tip floor TimeMs. Safe without
// sm.mu held (takes RLock).
func (sm *StateMachine) warnDeferredStampTimeMismatches() {
	if sm == nil {
		return
	}
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	sm.warnDeferredStampTimeMismatchesLocked()
}

func (sm *StateMachine) warnDeferredStampTimeMismatchesLocked() {
	src, ok := sm.storedHeadersLocked()
	if !ok {
		return
	}
	for id, rec := range sm.state.Inferences {
		if rec == nil {
			continue
		}
		if rec.StartedAtTimeMs > 0 && rec.StartedAtHeight > 0 {
			sm.warnRecordStampLocked(src, "started_at", id, rec.StartedAtHeight, rec.StartedAtBlockHash, rec.StartedAtTimeMs)
		}
		if rec.ConfirmedAtTimeMs > 0 && rec.ConfirmedAtHeight > 0 {
			sm.warnRecordStampLocked(src, "confirmed_at", id, rec.ConfirmedAtHeight, rec.ConfirmedAtBlockHash, rec.ConfirmedAtTimeMs)
		}
	}
	sm.warnFloorStampLocked(src)
}

// warnFloorStampLocked warns when the tip floor's carried TimeMs disagrees
// with a later map row for the same (height, hash). Does not rewrite the floor.
// Caller holds sm.mu.
func (sm *StateMachine) warnFloorStampLocked(src storedHeaderReader) {
	if !sm.floorReady || sm.heightSyncFloor == nil || src == nil {
		return
	}
	p, known := sm.heightSyncFloor.PointAsOf(sm.state.LatestNonce + 1)
	if !known || p.Height == 0 || p.TimeMs <= 0 {
		return
	}
	sm.warnStampVersusMapLocked(src, "floor", p.Height, p.Hash, p.TimeMs)
}

func (sm *StateMachine) warnRecordStampLocked(src storedHeaderReader, leg string, id, height uint64, hash []byte, stampMs int64) {
	sm.warnStampVersusMapLocked(src, fmt.Sprintf("%s:%d", leg, id), height, hash, stampMs)
}

func (sm *StateMachine) warnStampVersusMapLocked(src storedHeaderReader, leg string, height uint64, hash []byte, stampMs int64) {
	row, found := src.StoredAt(height)
	if !found || row.TimeMs <= 0 {
		return
	}
	if len(hash) > 0 && len(row.Hash) > 0 && !bytes.Equal(hash, row.Hash) {
		return
	}
	if row.TimeMs == stampMs {
		return
	}
	sm.emitStampTimeMismatchLocked(leg, height, hash, stampMs, row.TimeMs)
}

func (sm *StateMachine) emitStampTimeMismatchLocked(leg string, height uint64, hash []byte, stampMs, mapMs int64) {
	logging.Warn("accepted stamp time disagrees with header map",
		"subsystem", "state",
		"escrow_id", sm.state.EscrowID,
		"leg", leg,
		"height", height,
		"hash", hex.EncodeToString(hash),
		"stamp_time_ms", stampMs,
		"map_time_ms", mapMs,
	)
	if sm.stampTimeMismatchHook != nil {
		sm.stampTimeMismatchHook(leg, height, hash, stampMs, mapMs)
	}
}

// wireHeaderStampWatch installs a Watch callback that warns on deferred map
// disagrees. Cancel runs outside sm.mu.
func (sm *StateMachine) wireHeaderStampWatch(src HeaderTimeSource) {
	if sm == nil {
		return
	}
	sm.mu.Lock()
	cancel := sm.headerStampWatchCancel
	sm.headerStampWatchCancel = nil
	sm.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	w, ok := src.(HeaderTimeWatch)
	if !ok || w == nil {
		return
	}
	cancel = w.Watch(func() { sm.warnDeferredStampTimeMismatches() })
	sm.mu.Lock()
	sm.headerStampWatchCancel = cancel
	sm.mu.Unlock()
}

// SetStampTimeMismatchHook registers a callback for deferred map disagrees
// (tests). Nil clears it.
func (sm *StateMachine) SetStampTimeMismatchHook(fn func(leg string, height uint64, hash []byte, stampMs, mapMs int64)) {
	if sm == nil {
		return
	}
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.stampTimeMismatchHook = fn
}
