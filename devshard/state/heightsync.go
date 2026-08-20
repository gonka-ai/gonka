package state

import (
	"fmt"

	"devshard/heightsync"
	"devshard/types"
)

func countForceHeightSyncTurn(txs []*types.DevshardTx) int {
	n := 0
	for _, tx := range txs {
		if tx.GetForceHeightSyncTurn() != nil {
			n++
		}
	}
	return n
}

func (sm *StateMachine) clearExpiredHeightSyncFlags() {
	if sm.state.HeightSyncForcedEnd != 0 && sm.state.LatestNonce > sm.state.HeightSyncForcedEnd {
		sm.state.HeightSyncForcedStart = 0
		sm.state.HeightSyncForcedEnd = 0
	}
	if sm.state.HeightSyncCadenceSwallowUntil != 0 && sm.state.LatestNonce > sm.state.HeightSyncCadenceSwallowUntil {
		sm.state.HeightSyncCadenceSwallowUntil = 0
		sm.state.HeightSyncSwallowFe = 0
		sm.state.HeightSyncTurnK = 0
		sm.state.HeightSyncTurnSlots = 0
		sm.state.HeightSyncTurnReason = ""
	}
}

// HeightSyncEscrowHints returns height-sync scheduler hints from current escrow state.
func (sm *StateMachine) HeightSyncEscrowHints(defaultK, defaultSlots uint64) *heightsync.EscrowHeightSyncHints {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return heightsync.EscrowHintsFromState(sm.state, defaultK, defaultSlots)
}

// HeightSyncForcedTurnActive reports whether nextNonce would fall inside an active forced turn.
func (sm *StateMachine) HeightSyncForcedTurnActive(nextNonce uint64) bool {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	if sm.state.HeightSyncForcedEnd == 0 {
		return false
	}
	return nextNonce >= sm.state.HeightSyncForcedStart && nextNonce <= sm.state.HeightSyncForcedEnd
}

func (sm *StateMachine) applyForceHeightSyncTurn(msg *types.MsgForceHeightSyncTurn, diffNonce uint64) error {
	if msg == nil {
		return types.ErrEmptyTx
	}
	if sm.state.Phase != types.PhaseActive {
		return types.ErrSessionFinalizing
	}
	slots := uint64(len(sm.state.Group))
	if msg.TriggerNonce != diffNonce {
		return fmt.Errorf("%w: trigger_nonce must equal diff nonce", types.ErrInvalidNonce)
	}
	if msg.SlotsNum != slots {
		return fmt.Errorf("MsgForceHeightSyncTurn slots_num %d must equal group size %d", msg.SlotsNum, slots)
	}
	if msg.AnchorK < msg.SlotsNum {
		return heightsync.ErrInvalidConfig
	}
	expectedEnd := msg.TriggerNonce + msg.SlotsNum - 1
	if msg.EndNonce != expectedEnd {
		return fmt.Errorf("MsgForceHeightSyncTurn end_nonce must be trigger_nonce+slots_num-1")
	}
	// Ignore duplicate open while a forced turn still covers this diff nonce.
	if sm.state.HeightSyncForcedEnd != 0 && diffNonce <= sm.state.HeightSyncForcedEnd {
		return nil
	}

	sm.state.HeightSyncForcedStart = msg.TriggerNonce
	sm.state.HeightSyncForcedEnd = msg.EndNonce
	sm.state.HeightSyncTurnK = msg.AnchorK
	sm.state.HeightSyncTurnSlots = msg.SlotsNum
	sm.state.HeightSyncTurnReason = msg.Reason

	if swallowUntil, swallowFe, ok := heightsync.ComputeCadenceSwallow(msg.TriggerNonce, msg.EndNonce, msg.AnchorK, msg.SlotsNum); ok {
		sm.state.HeightSyncCadenceSwallowUntil = swallowUntil
		sm.state.HeightSyncSwallowFe = swallowFe
	} else {
		sm.state.HeightSyncCadenceSwallowUntil = 0
		sm.state.HeightSyncSwallowFe = 0
	}
	return nil
}

// applyHeartbeat accepts MsgHeartbeat into Diff. L0–L7 run in applyCore via CheckDiffLogPlane.
func (sm *StateMachine) applyHeartbeat(msg *types.MsgHeartbeat) error {
	if msg == nil {
		return types.ErrEmptyTx
	}
	if sm.state.Phase != types.PhaseActive {
		return types.ErrSessionFinalizing
	}
	return nil
}

// applyHeightAck accepts MsgHeightAck into Diff. Signature/causality checks run in applyCore.
func (sm *StateMachine) applyHeightAck(msg *types.MsgHeightAck) error {
	if msg == nil {
		return types.ErrEmptyTx
	}
	if sm.state.Phase != types.PhaseActive {
		return types.ErrSessionFinalizing
	}
	return nil
}

func (sm *StateMachine) checkLogPlaneLocked(nonce uint64, txs []*types.DevshardTx) error {
	res := heightsync.CheckDiffLogPlane(nil, heightsync.LogPlaneInput{
		Nonce: nonce,
		Txs:   txs,
	}, sm.logPlaneStateLocked())
	if res.Err != nil {
		return res.Err
	}
	if sm.heightSyncMarks != nil {
		sm.heightSyncMarks.AppendAll(res.Marks)
	}
	return nil
}

func (sm *StateMachine) logPlaneStateLocked() heightsync.LogPlaneState {
	return heightsync.LogPlaneState{
		SlotsNum:       uint64(len(sm.state.Group)),
		SlotKeys:       sm.slotToAddress,
		Verifier:       sm.verifier,
		Tracker:        sm.turnTracker,
		MaxStampHeight: sm.maxStampHeight,
		Cfg:            sm.heartbeatCfg,
		EscrowID:       sm.state.EscrowID,
	}
}

func (sm *StateMachine) observeHeightSyncLocked(nonce uint64, txs []*types.DevshardTx) {
	if sm.turnTracker == nil {
		return
	}
	var hNow uint64
	for _, tx := range txs {
		if tx == nil {
			continue
		}
		if hb := tx.GetHeartbeat(); hb != nil && heightsync.StampPresent(hb.ObservedBlockHash) && hb.ObservedHeight > hNow {
			hNow = hb.ObservedHeight
		}
		if ack := tx.GetHeightAck(); ack != nil && heightsync.StampPresent(ack.ObservedBlockHash) && ack.ObservedHeight > hNow {
			hNow = ack.ObservedHeight
		}
		if h, ok := heightsync.TxStamp(tx); ok && h > hNow {
			hNow = h
		}
	}
	if hNow == 0 {
		hNow = sm.turnTracker.LastCompletedHeight()
	}
	sm.turnTracker.Observe(nonce, txs, hNow)
	if hNow > sm.maxStampHeight {
		sm.maxStampHeight = hNow
	}
	sm.state.HeightSyncLastCompletedHeight = sm.turnTracker.LastCompletedHeight()
	sm.state.HeightSyncLatestTurnSeq = sm.turnTracker.MaxTurnSeq()
}

// HeightSyncRepairDue advances open turns to hNow and returns missing slots
// on the latest turn whose ack window has closed.
func (sm *StateMachine) HeightSyncRepairDue(hNow uint64) (turnSeq, spanStart uint64, missing []uint32) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if sm.turnTracker == nil || hNow == 0 {
		return 0, 0, nil
	}
	sm.turnTracker.AdvanceHeight(hNow)
	rec := sm.turnTracker.Latest()
	if rec == nil {
		return 0, 0, nil
	}
	return rec.TurnSeq, rec.RequestSpan[0], sm.turnTracker.MissingAcksDue(rec.TurnSeq, hNow)
}

// HeightSyncArmingContext is last complete turn_seq plus degraded turn ids.
func (sm *StateMachine) HeightSyncArmingContext() (lastComplete uint64, degraded []uint64) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	if sm.turnTracker == nil {
		return 0, nil
	}
	return sm.turnTracker.ArmingContext()
}

// HeightSyncMissingAcks is MissingAcksDue under the SM lock (stagger re-check).
func (sm *StateMachine) HeightSyncMissingAcks(turnSeq, hNow uint64) []uint32 {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	if sm.turnTracker == nil {
		return nil
	}
	return sm.turnTracker.MissingAcksDue(turnSeq, hNow)
}

// HeightSyncTurnRecord is a copy of the verifier-computed turn, or nil.
func (sm *StateMachine) HeightSyncTurnRecord(turnSeq uint64) *heightsync.SyncTurnRecord {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	if sm.turnTracker == nil {
		return nil
	}
	return sm.turnTracker.Record(turnSeq)
}

// HeightSyncMarks returns a copy of marks recorded on successful applyCore.
func (sm *StateMachine) HeightSyncMarks() []heightsync.AttributableMark {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	if sm.heightSyncMarks == nil {
		return nil
	}
	return sm.heightSyncMarks.All()
}
