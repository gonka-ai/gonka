package state

import (
	"context"
	"fmt"

	"devshard/heightsync"
	"devshard/logging"
	"devshard/types"
)

// logPlaneCtx is the context the apply path hands to CheckDiffLogPlane. Only
// L6 uses it, and only to reach the block oracle; neither call site supplies
// one, so nothing here can block or be cancelled. It stays a TODO because
// applyCore has no context of its own to plumb through yet.
func logPlaneCtx() context.Context { return context.TODO() }

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
	res := heightsync.CheckDiffLogPlane(logPlaneCtx(), heightsync.LogPlaneInput{
		Nonce: nonce,
		Txs:   txs,
	}, sm.logPlaneStateLocked())
	if res.Err != nil {
		heightsync.ObserveLogPlaneReject(res.Reason)
		return res.Err
	}
	sm.recordMarksLocked(res.Marks)
	return nil
}

// logPlaneErrLocked is L0–L3 without appending marks. Compose uses this to
// drop invalid height-sync txs before persist; marks stay on the applyCore
// path so a trial Preview cannot leak them into retained state.
//
// The reason travels back to the caller rather than being counted here: the
// caller re-checks a growing prefix once per tx, so only it knows which
// evaluation ended in an actual drop.
func (sm *StateMachine) logPlaneErrLocked(nonce uint64, txs []*types.DevshardTx) (string, error) {
	res := heightsync.CheckDiffLogPlane(logPlaneCtx(), heightsync.LogPlaneInput{
		Nonce: nonce,
		Txs:   txs,
	}, sm.logPlaneStateLocked())
	return res.Reason, res.Err
}

func (sm *StateMachine) recordMarksLocked(ms []heightsync.AttributableMark) {
	if len(ms) == 0 {
		return
	}
	if sm.marksDeferred != nil {
		*sm.marksDeferred = append(*sm.marksDeferred, ms...)
		return
	}
	sm.heightSyncMarks.AppendAll(ms)
}

func (sm *StateMachine) logPlaneStateLocked() heightsync.LogPlaneState {
	return heightsync.LogPlaneState{
		SlotsNum: uint64(len(sm.state.Group)),
		SlotKeys: sm.slotToAddress,
		Verifier: sm.verifier,
		Tracker:  sm.turnTracker,
		Floor:    sm.heightSyncFloor,
		Cfg:      sm.heartbeatCfg,
		EscrowID: sm.state.EscrowID,
	}
}

// HeightSyncFloorAsOf is F(m): the highest reference height stamped below nonce
// m, with its block hash. Producers use it to satisfy L0 — stamp
// max(own_tip, F(m)) or omit the stamp — so this is the read side of the rule
// the verifier enforces.
func (sm *StateMachine) HeightSyncFloorAsOf(nonce uint64) (uint64, []byte, bool) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	h, hash, known := sm.heightSyncFloor.AsOf(nonce)
	return h, append([]byte(nil), hash...), known
}

func (sm *StateMachine) observeHeightSyncLocked(nonce uint64, txs []*types.DevshardTx) {
	if sm.turnTracker == nil {
		return
	}
	sm.turnTracker.Observe(nonce, txs, heightsync.LogResidentHeight(txs, sm.turnTracker.LastCompletedHeight()))
	// Every Diff-resident height is a reference height and feeds L0, heartbeats
	// and acks included. Only host-signed first-party stamps raise F (spec §14
	// rule 3). Producers lift to F(m) or omit, so a party that is behind is
	// never put in an impossible position by another party's higher stamp; what
	// it may not do is write a height below the floor.
	//
	// The fold happens here, on apply, and nowhere else. That is deliberate: an
	// L5a refusal at the transport edge is a local admission decision about one
	// exchange, and the same diff arriving by catch-up or gossip carries no
	// envelope to refuse. Letting admission feed the floor would give two honest
	// verifiers different floors and therefore different L0 verdicts for every
	// later diff — an escrow split. Floor updates therefore run only on apply.
	if marks := sm.heightSyncFloor.Observe(nonce, sm.floorClaimsLocked(txs)); len(marks) > 0 {
		sm.recordMarksLocked(marks)
	}
	sm.state.HeightSyncLastCompletedHeight = sm.turnTracker.LastCompletedHeight()
	sm.state.HeightSyncLatestTurnSeq = sm.turnTracker.MaxTurnSeq()
}

// rebuildHeightSyncLocked reconstructs the turn tracker and floor from diffs
// 1..LatestNonce. Both are derived state: the floor's per-signer claims cannot
// be a persisted scalar, and the tracker is reconstructible from the log.
// RestoreState copies hashed HeightSync* fields from the snapshot; replaying
// MsgForceHeightSyncTurn fills them for legacy snapshots that omitted them.
// A GetDiffs error keeps those snapshot scalars (h_last / latest turn_seq)
// rather than folding an empty journal over them.
func (sm *StateMachine) rebuildHeightSyncLocked() {
	savedLast := sm.state.HeightSyncLastCompletedHeight
	savedSeq := sm.state.HeightSyncLatestTurnSeq
	slots := uint64(len(sm.state.Group))
	tracker := heightsync.NewTurnTracker(slots, 0, sm.heartbeatCfg)
	floor := heightsync.NewFloorIndexWith(
		heightsync.FloorConfigFor(len(sm.state.Group), sm.heartbeatCfg))
	if sm.state.LatestNonce == 0 || sm.inferenceStore == nil {
		sm.turnTracker = tracker
		sm.heightSyncFloor = floor
		return
	}
	records, err := sm.inferenceStore.GetDiffs(sm.state.EscrowID, 1, sm.state.LatestNonce)
	if err != nil {
		logging.Warn("heightsync: snapshot restore could not load diffs; keeping snapshot h_last",
			"escrow_id", sm.state.EscrowID, "error", err)
		tracker.SeedCompleted(savedLast, savedSeq)
		sm.turnTracker = tracker
		sm.heightSyncFloor = floor
		sm.state.HeightSyncLastCompletedHeight = savedLast
		sm.state.HeightSyncLatestTurnSeq = savedSeq
		return
	}
	sm.turnTracker = tracker
	sm.heightSyncFloor = floor
	for _, rec := range records {
		for _, tx := range rec.Txs {
			if msg := tx.GetForceHeightSyncTurn(); msg != nil {
				_ = sm.applyForceHeightSyncTurn(msg, rec.Nonce)
			}
		}
		sm.observeHeightSyncLocked(rec.Nonce, rec.Txs)
	}
	sm.clearExpiredHeightSyncFlags()
}

// floorClaimsLocked attributes each Diff-resident height to the identity that
// signed it, which is what the floor's raise rule counts.
//
// Every carrier is named in the log itself: `slot_id` on an ack (L2 verifies the
// signature over it), `executor_slot` on a finish, the executor of record for a
// confirm — the assignment the state machine made at PrepareInference, so no
// wire field is added — and the sequencer for the legs it composes. A leg the
// log cannot attribute is still judged by L0; it simply does not vote on where
// the floor goes.
func (sm *StateMachine) floorClaimsLocked(txs []*types.DevshardTx) []heightsync.FloorClaim {
	claims := make([]heightsync.FloorClaim, 0, len(txs))
	for _, tx := range txs {
		h, hash, ok := heightsync.RefStamp(tx)
		if !ok {
			continue
		}
		signer := heightsync.SequencerSigner
		switch {
		case tx.GetHeightAck() != nil:
			signer = tx.GetHeightAck().SlotId
		case tx.GetFinishInference() != nil:
			signer = tx.GetFinishInference().ExecutorSlot
		case tx.GetConfirmStart() != nil:
			rec := sm.state.Inferences[tx.GetConfirmStart().InferenceId]
			if rec == nil {
				continue
			}
			signer = rec.ExecutorSlot
		}
		claims = append(claims, heightsync.FloorClaim{Signer: signer, Height: h, Hash: hash})
	}
	return claims
}

// HeightSyncLatestTurnSeq is the highest turn_seq folded from Diff. RecoverSession
// restores the producer counter from this; it is derived and not hashed.
func (sm *StateMachine) HeightSyncLatestTurnSeq() uint64 {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.state.HeightSyncLatestTurnSeq
}

// HeightSyncCloneTurnTracker returns a copy of the log-folded tracker so a
// recovered producer can keep composing without sharing the SM mutex.
func (sm *StateMachine) HeightSyncCloneTurnTracker() *heightsync.TurnTracker {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	if sm.turnTracker == nil {
		return nil
	}
	return sm.turnTracker.Clone()
}

// HeightSyncRepairDue returns every retained turn whose ack window has closed
// with missing slots. Spec §11.3 probes turn s, not only Latest(). The probe
// still carries the local tip on the wire; this does not AdvanceHeight with it.
func (sm *StateMachine) HeightSyncRepairDue() []heightsync.RepairDue {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	if sm.turnTracker == nil {
		return nil
	}
	return sm.turnTracker.RepairDueAll()
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

// HeightSyncMissingAcks is MissingAcksDue under the SM lock (stagger re-check),
// keyed on h_last rather than a live oracle height.
func (sm *StateMachine) HeightSyncMissingAcks(turnSeq uint64) []uint32 {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	if sm.turnTracker == nil {
		return nil
	}
	return sm.turnTracker.MissingAcksDue(turnSeq, sm.turnTracker.LastCompletedHeight())
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
