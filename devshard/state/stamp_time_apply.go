package state

import (
	"bytes"
	"encoding/hex"
	"fmt"

	"devshard/heightsync"
	"devshard/logging"
	"devshard/types"
)

// stampTimeRaiseDeferredMsg is the log line for a host stamp whose carried
// time is more than StampTimeRaiseDelta ahead of the previous floor time.
// The floor entry keeps that carried time.
const stampTimeRaiseDeferredMsg = "host stamp time is more than 45s ahead of the floor; strong check deferred"

// checkObservedStampTimeLocked checks a stamp while applying it.
// On StampRequiresTime tags, a stamped message (height > 0) with
// observed_time_ms <= 0 is ErrStampTimeMissing (never ErrHeaderPending).
// Earlier tags still accept 0. A map miss keeps the carried time. A map
// hit that disagrees is a warning: apply keeps the carried time, and a
// later dispute rolls back a timeout decided on it. Compose still rejects
// that disagreement before signing. Never waits and never calls HeaderTimeMs.
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
	sm.warnStampVersusMapLocked(src, leg, height, hash, observedTimeMs)
	return nil
}

// latestHeaderClock is the newest block a header map still holds.
// *headertime.Source implements it. A source that only answers one height
// does not, and the newest-block check is skipped.
type latestHeaderClock interface {
	LatestStored() (height uint64, timeMs int64, ok bool)
}

// checkExecutorStampClockLocked compares an executor confirm or finish clock
// with the floor already in the log and with the newest block in the local
// header map. Caller holds sm.mu.
//
// A carried time before the previous floor needs the strong header proof.
// That proof is deferred, so this only logs.
//
// A carried time before the newest stored block, on a stamp at or above that
// block, is a local proof the executor backdated the stamp. Compose and
// challenge evidence reject the confirm or finish. Apply only logs it: the
// header map is local to each replica, so a reject there would make the
// replicas disagree on a signed diff. A later dispute rolls back a timeout
// decided on this time. A stamp below the newest stored block can still carry
// the floor while the poller has moved on, so that case stays on the floor rule.
func (sm *StateMachine) checkExecutorStampClockLocked(stage, leg string, height uint64, carried int64) error {
	if sm == nil || carried <= 0 {
		return nil
	}
	if err := sm.checkStampNotFutureLocked(stage, leg, height, carried); err != nil {
		return err
	}
	floorH, floorMs := sm.previousFloorClockLocked()
	latestH, latestMs, latestOK := sm.latestStoredClockLocked()
	beforeFloor := floorMs > 0 && carried < floorMs
	beforeLatest := latestOK && latestMs > 0 && height > 0 && height >= latestH && carried < latestMs
	if beforeFloor {
		logging.Warn("executor stamp time is before the floor; strong check deferred",
			"subsystem", "state",
			"stage", stage,
			"leg", leg,
			"escrow_id", sm.state.EscrowID,
			"height", height,
			"carried_time_ms", carried,
			"floor_height", floorH,
			"floor_time_ms", floorMs,
		)
	}
	if !beforeLatest {
		return nil
	}
	logging.Warn("executor stamp time is before the newest stored block",
		"subsystem", "state",
		"stage", stage,
		"leg", leg,
		"escrow_id", sm.state.EscrowID,
		"height", height,
		"carried_time_ms", carried,
		"latest_height", latestH,
		"latest_time_ms", latestMs,
		"floor_height", floorH,
		"floor_time_ms", floorMs,
	)
	if stage == "apply" {
		return nil
	}
	return fmt.Errorf("%w: %s height %d carried=%d latest_height=%d latest_time_ms=%d",
		types.ErrStampTimeBeforeChain, leg, height, carried, latestH, latestMs)
}

// checkStampNotFutureLocked rejects a host stamp dated more than
// StampFutureSkew ahead of the wall clock installed on this machine.
// Apply skips it: replicas with different clocks would split. A nil clock
// skips it, so replay and tests stay clock-free. Caller holds sm.mu.
func (sm *StateMachine) checkStampNotFutureLocked(stage, leg string, height uint64, carried int64) error {
	if sm == nil || carried <= 0 || stage == "apply" || sm.stampWallClock == nil {
		return nil
	}
	now := sm.stampWallClock()
	if now.IsZero() {
		return nil
	}
	limit := now.UnixMilli() + heightsync.StampFutureSkew.Milliseconds()
	if carried <= limit {
		return nil
	}
	return fmt.Errorf("%w: %s height %d carried=%d wall_ms=%d",
		types.ErrStampTimeInFuture, leg, height, carried, now.UnixMilli())
}

// noteStampTimeRaiseLocked logs a confirm or finish whose carried time is
// more than StampTimeRaiseDelta ahead of the previous floor. It does not
// change the stamp or the floor. Caller holds sm.mu.
func (sm *StateMachine) noteStampTimeRaiseLocked(stage, leg string, height uint64, carried int64) {
	if sm == nil || carried <= 0 {
		return
	}
	floorH, floorMs := sm.previousFloorClockLocked()
	if !timeRaisePastDelta(floorMs, carried) {
		return
	}
	logging.Warn(stampTimeRaiseDeferredMsg,
		"subsystem", "state",
		"stage", stage,
		"leg", leg,
		"escrow_id", sm.state.EscrowID,
		"height", height,
		"floor_height", floorH,
		"prev_time_ms", floorMs,
		"carried_time_ms", carried,
	)
}

func timeRaisePastDelta(prev, carried int64) bool {
	if prev <= 0 || carried <= 0 {
		return false
	}
	delta := heightsync.StampTimeRaiseDelta.Milliseconds()
	return carried > prev && carried-prev > delta
}

func (sm *StateMachine) checkTxExecutorStampClockLocked(stage string, tx *types.DevshardTx) error {
	if tx == nil {
		return nil
	}
	switch {
	case tx.GetConfirmStart() != nil:
		msg := tx.GetConfirmStart()
		return sm.checkExecutorStampClockLocked(stage, "confirm", msg.GetObservedHeight(), msg.GetObservedTimeMs())
	case tx.GetFinishInference() != nil:
		msg := tx.GetFinishInference()
		return sm.checkExecutorStampClockLocked(stage, "finish", msg.GetObservedHeight(), msg.GetObservedTimeMs())
	default:
		return nil
	}
}

// previousFloorClockLocked is the floor as of the next diff, before this
// diff's stamps are observed. The map time wins when the stored hash agrees.
func (sm *StateMachine) previousFloorClockLocked() (height uint64, timeMs int64) {
	p, err := sm.floorPointLocked(sm.state.LatestNonce + 1)
	if err != nil || p.Height == 0 {
		return 0, 0
	}
	if src, ok := sm.storedHeadersLocked(); ok {
		if row, found := src.StoredAt(p.Height); found && row.TimeMs > 0 && stampHashesAgree(p.Hash, row.Hash) {
			return p.Height, row.TimeMs
		}
	}
	if p.TimeMs > 0 {
		return p.Height, p.TimeMs
	}
	return p.Height, 0
}

func (sm *StateMachine) latestStoredClockLocked() (height uint64, timeMs int64, ok bool) {
	src, isClock := sm.headerTimes.(latestHeaderClock)
	if !isClock || src == nil {
		return 0, 0, false
	}
	return src.LatestStored()
}

func stampHashesAgree(a, b []byte) bool {
	if len(a) == 0 || len(b) == 0 {
		return true
	}
	return bytes.Equal(a, b)
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
