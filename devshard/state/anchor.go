package state

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"time"

	"devshard/heightsync"
	"devshard/logging"
	"devshard/types"
)

// errAckHashMismatch is a host ack whose hash disagrees with a header this
// process already stored, or with a host ack already accepted at that height.
// It is not ErrHeaderPending: compose leaves the ack out, and does not retry it.
// A later version should open a dispute from this disagreement.
var errAckHashMismatch = errors.New("host ack hash disagrees with the stored header")

func (sm *StateMachine) floorPointLocked(m uint64) (heightsync.FloorPoint, error) {
	if !sm.floorReady {
		return heightsync.FloorPoint{}, types.ErrFloorNotRestored
	}
	p, known := sm.heightSyncFloor.PointAsOf(m)
	if !known {
		return heightsync.FloorPoint{}, fmt.Errorf("%w: floor as of nonce %d is pruned", types.ErrStampNotFloor, m)
	}
	if p.Height == 0 || !heightsync.StampPresent(p.Hash) {
		return heightsync.FloorPoint{}, nil
	}
	return p, nil
}

func (sm *StateMachine) checkFloorStampLocked(leg string, m, height uint64, hash []byte) (heightsync.FloorPoint, error) {
	p, err := sm.floorPointLocked(m)
	if err != nil {
		return p, err
	}
	stamped := heightsync.StampPresent(hash)
	if p.Height == 0 {
		// v7 and later reject a start while F is empty, stamped or not. An
		// earlier tag still applies an empty first start so a stored session
		// replays. A stamp on an empty floor was already rejected.
		if leg == "start" && types.StartRequiresFloor(sm.state.StateRootAndProtocolVersion) {
			return p, fmt.Errorf("%w: %s stamp %d, floor as of nonce %d is empty", types.ErrStampNotFloor, leg, height, m)
		}
		if stamped || height != 0 {
			return p, fmt.Errorf("%w: %s stamp %d, floor as of nonce %d is empty", types.ErrStampNotFloor, leg, height, m)
		}
		return p, nil
	}
	if !stamped || height != p.Height || !bytes.Equal(hash, p.Hash) {
		return p, fmt.Errorf("%w: %s stamp %d, floor as of nonce %d is %d", types.ErrStampNotFloor, leg, height, m, p.Height)
	}
	return p, nil
}

func StartStamp(rec *types.InferenceRecord) (uint64, []byte) {
	if rec == nil || rec.StartedAtHeight == 0 || !heightsync.StampPresent(rec.StartedAtBlockHash) {
		return 0, nil
	}
	return rec.StartedAtHeight, append([]byte(nil), rec.StartedAtBlockHash...)
}

// checkStartStamp admits the executor's confirm.
//
// A start with no floor may be confirmed at the host's tip: that stamp is what
// first raises the floor. A start that already carried a floor may be confirmed
// at that same block, or at a newer one. A lower height, or the same height on
// a different block, is rejected. An empty confirm is still legal when the
// start itself carried no floor.
func checkStartStamp(rec *types.InferenceRecord, height uint64, hash []byte) error {
	want, wantHash := StartStamp(rec)
	present := heightsync.StampPresent(hash)
	if height == 0 && !present {
		if want == 0 {
			return nil
		}
		return fmt.Errorf("%w: confirm stamp %d, start floor %d", types.ErrStampNotFloor, height, want)
	}
	if height == 0 || !present {
		return fmt.Errorf("%w: confirm stamp %d, start floor %d", types.ErrStampNotFloor, height, want)
	}
	if want == 0 || height > want {
		return nil
	}
	if height == want && bytes.Equal(hash, wantHash) {
		return nil
	}
	return fmt.Errorf("%w: confirm stamp %d, start floor %d", types.ErrStampNotFloor, height, want)
}

func (sm *StateMachine) floorTimeMsLocked(p heightsync.FloorPoint) (int64, error) {
	if p.Height == 0 {
		return 0, nil
	}
	if sm.headerTimes == nil {
		return 0, fmt.Errorf("%w: no header time source for height %d", types.ErrHeaderPending, p.Height)
	}
	ms, err := sm.headerTimes.HeaderTimeMs(p.Height, p.Hash)
	if errors.Is(err, types.ErrHeaderPending) && sm.replayingPersisted {
		if awaiter, ok := sm.headerTimes.(HeaderTimeAwaiter); ok {
			ctx, cancel := context.WithTimeout(context.Background(), ReplayHeaderAwait)
			ms, err = awaiter.AwaitHeaderTimeMs(ctx, p.Height, p.Hash)
			cancel()
		}
	}
	if err != nil {
		return 0, err
	}
	if ms <= 0 {
		return 0, fmt.Errorf("%w: height %d has no block time", types.ErrHeaderPending, p.Height)
	}
	return ms, nil
}

// HeightAckHashDisagrees reports an ack that must not enter the log.
// A missing row is not a mismatch. The error is not ErrHeaderPending.
func (sm *StateMachine) HeightAckHashDisagrees(ack *types.MsgHeightAck) error {
	if sm == nil {
		return nil
	}
	sm.mu.Lock()
	defer sm.mu.Unlock()
	return sm.ackHashMismatchLocked(ack)
}

// ackHashMismatchLocked reports an ack that must not enter the log.
// A missing row is not a mismatch: the poller may still be catching up, and
// the ack applies. The check runs on compose. A replica applying a committed
// diff still records the ack and logs the disagreement from NoteHostAck.
func (sm *StateMachine) ackHashMismatchLocked(ack *types.MsgHeightAck) error {
	if sm == nil || ack == nil || ack.ObservedHeight == 0 || !heightsync.StampPresent(ack.ObservedBlockHash) {
		return nil
	}
	view, ok := sm.headerTimes.(HeaderAckView)
	if !ok || view == nil {
		return nil
	}
	if hash, found := view.StoredHash(ack.ObservedHeight); found && !bytes.Equal(hash, ack.ObservedBlockHash) {
		// A later version should open a dispute from this disagreement.
		logging.Warn("host ack hash disagrees with the polled header",
			"subsystem", "state",
			"height", ack.ObservedHeight,
			"slot", ack.SlotId,
			"ack_hash", hex.EncodeToString(ack.ObservedBlockHash),
			"polled_hash", hex.EncodeToString(hash))
		return fmt.Errorf("%w: height %d", errAckHashMismatch, ack.ObservedHeight)
	}
	if hash, found := view.HostAckHash(ack.ObservedHeight); found && !bytes.Equal(hash, ack.ObservedBlockHash) {
		// A later version should open a dispute from this disagreement.
		logging.Warn("host ack hash disagrees with an earlier host ack",
			"subsystem", "state",
			"height", ack.ObservedHeight,
			"slot", ack.SlotId,
			"ack_hash", hex.EncodeToString(ack.ObservedBlockHash),
			"host_hash", hex.EncodeToString(hash))
		return fmt.Errorf("%w: height %d", errAckHashMismatch, ack.ObservedHeight)
	}
	return nil
}

func (sm *StateMachine) noteAcceptedHostAckLocked(ack *types.MsgHeightAck) {
	if sm == nil || ack == nil || ack.ObservedHeight == 0 || !heightsync.StampPresent(ack.ObservedBlockHash) {
		return
	}
	view, ok := sm.headerTimes.(HeaderAckView)
	if !ok || view == nil {
		return
	}
	view.NoteHostAck(ack.ObservedHeight, ack.ObservedBlockHash)
}

func (sm *StateMachine) checkRaiseResolvableLocked(nonce uint64, tx *types.DevshardTx) error {
	if tx.GetHeightAck() != nil {
		// The host signature raises F. A missing polled time means this
		// process is still catching up; it does not hold the ack.
		return nil
	}
	cur, _ := sm.heightSyncFloor.Max()
	for _, c := range floorClaims(sm.state, []*types.DevshardTx{tx}) {
		if c.Signer == heightsync.SequencerSigner || c.Height <= cur || !heightsync.StampPresent(c.Hash) {
			continue
		}
		if _, err := sm.floorTimeMsLocked(heightsync.FloorPoint{Height: c.Height, Hash: c.Hash}); err != nil {
			return fmt.Errorf("floor claim %d at nonce %d: %w", c.Height, nonce, err)
		}
	}
	return nil
}

func secondsToMs(seconds int64) int64 {
	if seconds <= 0 {
		return 0
	}
	if seconds > math.MaxInt64/1000 {
		return math.MaxInt64
	}
	return seconds * 1000
}

func (sm *StateMachine) timeoutWindowLocked(rec *types.InferenceRecord, reason types.TimeoutReason) (anchorMs, windowMs int64, err error) {
	switch reason {
	case types.TimeoutReason_TIMEOUT_REASON_REFUSED:
		if rec.Status != types.StatusPending {
			return 0, 0, fmt.Errorf("%w: reason=refused requires pending, got %d", types.ErrInvalidTimeoutReason, rec.Status)
		}
		anchorMs, windowMs = rec.StartedAtTimeMs, secondsToMs(sm.state.Config.RefusalTimeout)
	case types.TimeoutReason_TIMEOUT_REASON_EXECUTION:
		if rec.Status != types.StatusStarted {
			return 0, 0, fmt.Errorf("%w: reason=execution requires started, got %d", types.ErrInvalidTimeoutReason, rec.Status)
		}
		anchorMs, windowMs = rec.ConfirmedAtTimeMs, secondsToMs(sm.state.Config.ExecutionTimeout)
	default:
		return 0, 0, fmt.Errorf("%w: unknown reason %v", types.ErrInvalidTimeoutReason, reason)
	}
	if anchorMs <= 0 {
		return 0, 0, fmt.Errorf("%w: reason=%s", types.ErrTimeoutNotArmed, reason)
	}
	return anchorMs, windowMs, nil
}

func (sm *StateMachine) timeoutRemainingLocked(rec *types.InferenceRecord, reason types.TimeoutReason, nonce uint64) (time.Duration, error) {
	anchorMs, windowMs, err := sm.timeoutWindowLocked(rec, reason)
	if err != nil {
		return 0, err
	}
	p, err := sm.floorPointLocked(nonce)
	if err != nil {
		return 0, err
	}
	nowMs, err := sm.floorTimeMsLocked(p)
	if err != nil {
		return 0, err
	}
	elapsed := nowMs - anchorMs
	if elapsed >= windowMs {
		return 0, nil
	}
	maxRemainingMs := int64(time.Duration(math.MaxInt64) / time.Millisecond)
	if elapsed < windowMs-maxRemainingMs {
		return time.Duration(math.MaxInt64), nil
	}
	return time.Duration(windowMs-elapsed) * time.Millisecond, nil
}

func (sm *StateMachine) checkTimeoutDueLocked(rec *types.InferenceRecord, reason types.TimeoutReason, nonce uint64) error {
	remaining, err := sm.timeoutRemainingLocked(rec, reason, nonce)
	if err != nil {
		return err
	}
	if remaining > 0 {
		return fmt.Errorf("%w: reason=%s needs %s more block time at nonce %d", types.ErrTimeoutNotDue, reason, remaining, nonce)
	}
	return nil
}

func (sm *StateMachine) TimeoutRemaining(inferenceID uint64, reason types.TimeoutReason) (time.Duration, error) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	rec, ok := sm.state.Inferences[inferenceID]
	if !ok {
		return 0, fmt.Errorf("%w: inference %d", types.ErrInferenceNotFound, inferenceID)
	}
	return sm.timeoutRemainingLocked(rec, reason, sm.state.LatestNonce+1)
}

func (sm *StateMachine) TimeoutDue(inferenceID uint64, reason types.TimeoutReason) error {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	rec, ok := sm.state.Inferences[inferenceID]
	if !ok {
		return fmt.Errorf("%w: inference %d", types.ErrInferenceNotFound, inferenceID)
	}
	return sm.checkTimeoutDueLocked(rec, reason, sm.state.LatestNonce+1)
}
