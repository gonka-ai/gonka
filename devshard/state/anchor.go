package state

import (
	"bytes"
	"fmt"
	"time"

	"devshard/heightsync"
	"devshard/types"
)

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

func checkStartStamp(rec *types.InferenceRecord, height uint64, hash []byte) error {
	want, wantHash := StartStamp(rec)
	stamped := heightsync.StampPresent(hash)
	if want == 0 {
		if stamped || height != 0 {
			return fmt.Errorf("%w: confirm stamp %d, start carried no floor", types.ErrStampNotFloor, height)
		}
		return nil
	}
	if !stamped || height != want || !bytes.Equal(hash, wantHash) {
		return fmt.Errorf("%w: confirm stamp %d, start floor %d", types.ErrStampNotFloor, height, want)
	}
	return nil
}

func (sm *StateMachine) timeoutBlocks(seconds int64) uint64 {
	if seconds <= 0 {
		return 0
	}
	blockTime := sm.heartbeatCfg.BlockTime
	if blockTime <= 0 {
		blockTime = heightsync.DefaultBlockTime
	}
	d := time.Duration(seconds) * time.Second
	return uint64((d + blockTime - 1) / blockTime)
}

func (sm *StateMachine) timeoutWindowLocked(rec *types.InferenceRecord, reason types.TimeoutReason) (anchor, blocks uint64, err error) {
	switch reason {
	case types.TimeoutReason_TIMEOUT_REASON_REFUSED:
		if rec.Status != types.StatusPending {
			return 0, 0, fmt.Errorf("%w: reason=refused requires pending, got %d", types.ErrInvalidTimeoutReason, rec.Status)
		}
		anchor, blocks = rec.StartedAtHeight, sm.timeoutBlocks(sm.state.Config.RefusalTimeout)
	case types.TimeoutReason_TIMEOUT_REASON_EXECUTION:
		if rec.Status != types.StatusStarted {
			return 0, 0, fmt.Errorf("%w: reason=execution requires started, got %d", types.ErrInvalidTimeoutReason, rec.Status)
		}
		anchor, blocks = rec.ConfirmedAtHeight, sm.timeoutBlocks(sm.state.Config.ExecutionTimeout)
	default:
		return 0, 0, fmt.Errorf("%w: unknown reason %v", types.ErrInvalidTimeoutReason, reason)
	}
	if anchor == 0 {
		return 0, 0, fmt.Errorf("%w: reason=%s", types.ErrTimeoutNotArmed, reason)
	}
	return anchor, blocks, nil
}

func (sm *StateMachine) timeoutRemainingLocked(rec *types.InferenceRecord, reason types.TimeoutReason, nonce uint64) (uint64, error) {
	anchor, blocks, err := sm.timeoutWindowLocked(rec, reason)
	if err != nil {
		return 0, err
	}
	p, err := sm.floorPointLocked(nonce)
	if err != nil {
		return 0, err
	}
	due := addSatUint64(anchor, blocks)
	if p.Height >= due {
		return 0, nil
	}
	return due - p.Height, nil
}

func (sm *StateMachine) checkTimeoutDueLocked(rec *types.InferenceRecord, reason types.TimeoutReason, nonce uint64) error {
	remaining, err := sm.timeoutRemainingLocked(rec, reason, nonce)
	if err != nil {
		return err
	}
	if remaining > 0 {
		return fmt.Errorf("%w: reason=%s needs %d more blocks at nonce %d", types.ErrTimeoutNotDue, reason, remaining, nonce)
	}
	return nil
}

func (sm *StateMachine) TimeoutRemaining(inferenceID uint64, reason types.TimeoutReason) (uint64, error) {
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

func (sm *StateMachine) TimeoutDueHeight(inferenceID uint64, reason types.TimeoutReason) (uint64, error) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	rec, ok := sm.state.Inferences[inferenceID]
	if !ok {
		return 0, fmt.Errorf("%w: inference %d", types.ErrInferenceNotFound, inferenceID)
	}
	anchor, blocks, err := sm.timeoutWindowLocked(rec, reason)
	if err != nil {
		return 0, err
	}
	return addSatUint64(anchor, blocks), nil
}

func (sm *StateMachine) BlocksToDuration(n uint64) time.Duration {
	blockTime := sm.heartbeatCfg.BlockTime
	if blockTime <= 0 {
		blockTime = heightsync.DefaultBlockTime
	}
	return time.Duration(n) * blockTime
}

func addSatUint64(a, b uint64) uint64 {
	if a > ^uint64(0)-b {
		return ^uint64(0)
	}
	return a + b
}
