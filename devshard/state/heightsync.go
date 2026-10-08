package state

import (
	"context"
	"fmt"

	"devshard/heightsync"
	"devshard/logging"
	"devshard/storage"
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
	clearExpiredHeightSyncFlagsOf(sm.state)
}

func clearExpiredHeightSyncFlagsOf(st *types.EscrowState) {
	if st.HeightSyncForcedEnd != 0 && st.LatestNonce > st.HeightSyncForcedEnd {
		st.HeightSyncForcedStart = 0
		st.HeightSyncForcedEnd = 0
	}
	if st.HeightSyncCadenceSwallowUntil != 0 && st.LatestNonce > st.HeightSyncCadenceSwallowUntil {
		st.HeightSyncCadenceSwallowUntil = 0
		st.HeightSyncSwallowFe = 0
		st.HeightSyncTurnK = 0
		st.HeightSyncTurnSlots = 0
		st.HeightSyncTurnReason = ""
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
	return applyForceHeightSyncTurnTo(sm.state, msg, diffNonce)
}

func applyForceHeightSyncTurnTo(st *types.EscrowState, msg *types.MsgForceHeightSyncTurn, diffNonce uint64) error {
	if msg == nil {
		return types.ErrEmptyTx
	}
	if st.Phase != types.PhaseActive {
		return types.ErrSessionFinalizing
	}
	slots := uint64(len(st.Group))
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
	if st.HeightSyncForcedEnd != 0 && diffNonce <= st.HeightSyncForcedEnd {
		return nil
	}

	st.HeightSyncForcedStart = msg.TriggerNonce
	st.HeightSyncForcedEnd = msg.EndNonce
	st.HeightSyncTurnK = msg.AnchorK
	st.HeightSyncTurnSlots = msg.SlotsNum
	st.HeightSyncTurnReason = msg.Reason

	if swallowUntil, swallowFe, ok := heightsync.ComputeCadenceSwallow(msg.TriggerNonce, msg.EndNonce, msg.AnchorK, msg.SlotsNum); ok {
		st.HeightSyncCadenceSwallowUntil = swallowUntil
		st.HeightSyncSwallowFe = swallowFe
	} else {
		st.HeightSyncCadenceSwallowUntil = 0
		st.HeightSyncSwallowFe = 0
	}
	return nil
}

// applyHeartbeat accepts MsgHeartbeat into Diff while Active. L0–L7 run in
// applyCore via CheckDiffLogPlane. Compose skips these once Finalizing.
func (sm *StateMachine) applyHeartbeat(msg *types.MsgHeartbeat) error {
	if msg == nil {
		return types.ErrEmptyTx
	}
	if sm.state.Phase != types.PhaseActive {
		return types.ErrSessionFinalizing
	}
	return nil
}

// applyHeightAck accepts MsgHeightAck into Diff while Active. Signature/causality
// checks run in applyCore. Compose skips these once Finalizing.
//
// A first-time warm ack is admitted by L2 via AcceptWarm (CheckWarmKey) before
// this runs. Cache the binding here so WarmKeyDelta captures it for replay,
// matching ResolveWarmKey on confirm/finish/vote. Failure to cache is not an
// apply error: L2 already accepted the signer (sibling binding or live authz).
func (sm *StateMachine) applyHeightAck(msg *types.MsgHeightAck) error {
	if msg == nil {
		return types.ErrEmptyTx
	}
	if sm.state.Phase != types.PhaseActive {
		return types.ErrSessionFinalizing
	}
	expected, ok := sm.slotToAddress[msg.SlotId]
	if !ok {
		return fmt.Errorf("%w: slot %d", types.ErrSlotNotInGroup, msg.SlotId)
	}
	recovered, err := heightsync.RecoverAckSigner(sm.verifier, msg)
	if err != nil {
		return err
	}
	if recovered != expected {
		sm.ResolveWarmKey(msg.SlotId, recovered, expected)
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
		WarmKeys: sm.state.WarmKeys,
		AcceptWarm: func(slotID uint32, recovered, expected string) bool {
			_ = slotID
			return sm.CheckWarmKey(recovered, expected)
		},
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

// HeightSyncFloorReady reports whether the floor is a consistent fold of
// 1..LatestNonce. Producers omit stamps when this is false rather than
// writing a raw oracle tip against a missing F.
func (sm *StateMachine) HeightSyncFloorReady() bool {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.floorReady
}

// ExportHeightSyncFloor copies the derived floor for the snapshot envelope.
//
// A floor that never folded 1..LatestNonce is omitted rather than written out
// empty. Restore installs a present blob as authoritative, so persisting one
// from a not-ready machine would launder "we could not rebuild the fold" into
// "the fold is empty" — the split this blob exists to prevent.
func (sm *StateMachine) ExportHeightSyncFloor() *types.FloorIndexProto {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	if sm.heightSyncFloor == nil || !sm.floorReady {
		return nil
	}
	return sm.heightSyncFloor.ToProto()
}

func (sm *StateMachine) observeHeightSyncLocked(nonce uint64, txs []*types.DevshardTx) {
	if sm.turnTracker == nil {
		return
	}
	sm.journalHeightSyncLocked(txs)
	observeHeightSync(sm.state, sm.turnTracker, sm.heightSyncFloor, nonce, txs)
}

func observeHeightSync(st *types.EscrowState, tracker *heightsync.TurnTracker, floor *heightsync.FloorIndex, nonce uint64, txs []*types.DevshardTx) {
	tracker.Observe(nonce, txs, heightsync.LogResidentHeight(txs, tracker.LastCompletedHeight()))
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
	floor.Observe(nonce, floorClaims(st, txs))
	st.HeightSyncLastCompletedHeight = tracker.LastCompletedHeight()
	st.HeightSyncLatestTurnStart = tracker.LatestTurnStart()
}

// heightSyncRestore is the tracker and floor a restore installs.
type heightSyncRestore struct {
	tracker *heightsync.TurnTracker
	floor   *heightsync.FloorIndex
	ready   bool
	err     error
}

func (r heightSyncRestore) installLocked(sm *StateMachine) error {
	sm.turnTracker = r.tracker
	sm.heightSyncFloor = r.floor
	sm.floorReady = r.ready
	return r.err
}

// foldHeightSync reconstructs the turn tracker and floor for a restored state.
// st must be private to the caller: the fold writes its height-sync fields and
// runs without sm.mu, so a long journal does not block the state machine.
//
// The journal is the canonical fold (tracker + floor). A snapshot floor is
// installed only when that replay cannot run, so L0 still agrees after a
// GetDiffs failure. If LatestNonce > 0 and neither source works, restore
// fails: an empty floor would skip L0 and split the escrow from any replica
// that rebuilt.
func foldHeightSync(store storage.DiffReader, st *types.EscrowState, hbCfg heightsync.HeartbeatConfig, snapFloor *heightsync.FloorIndex) heightSyncRestore {
	savedLast := st.HeightSyncLastCompletedHeight
	savedStart := st.HeightSyncLatestTurnStart
	slots := uint64(len(st.Group))
	tracker := heightsync.NewTurnTracker(slots, 0, hbCfg)
	cfg := heightsync.FloorConfig{}
	emptyFloor := heightsync.NewFloorIndexWith(cfg)

	restore := func(floor *heightsync.FloorIndex, ready bool, err error) heightSyncRestore {
		st.HeightSyncLastCompletedHeight = savedLast
		st.HeightSyncLatestTurnStart = savedStart
		return heightSyncRestore{tracker: tracker, floor: floor, ready: ready, err: err}
	}

	if st.LatestNonce == 0 {
		return restore(emptyFloor, true, nil)
	}

	if store != nil {
		// The snapshot already restored these flags. A page that fails after
		// an earlier page applied a force turn must not leave that prefix in
		// place: the snapshot floor path below puts the restored flags back.
		savedForcedStart := st.HeightSyncForcedStart
		savedForcedEnd := st.HeightSyncForcedEnd
		savedCadence := st.HeightSyncCadenceSwallowUntil
		savedSwallowFe := st.HeightSyncSwallowFe
		savedTurnK := st.HeightSyncTurnK
		savedTurnSlots := st.HeightSyncTurnSlots
		savedReason := st.HeightSyncTurnReason

		foldTracker := heightsync.NewTurnTracker(slots, 0, hbCfg)
		foldFloor := heightsync.NewFloorIndexWith(cfg)
		err := storage.ReadDiffPages(store, st.EscrowID, 1, st.LatestNonce, func(page []types.DiffRecord) error {
			for _, rec := range page {
				for _, tx := range rec.Txs {
					if msg := tx.GetForceHeightSyncTurn(); msg != nil {
						_ = applyForceHeightSyncTurnTo(st, msg, rec.Nonce)
					}
				}
				observeHeightSync(st, foldTracker, foldFloor, rec.Nonce, rec.Txs)
			}
			return nil
		})
		if err == nil {
			clearExpiredHeightSyncFlagsOf(st)
			return heightSyncRestore{tracker: foldTracker, floor: foldFloor, ready: true}
		}
		st.HeightSyncForcedStart = savedForcedStart
		st.HeightSyncForcedEnd = savedForcedEnd
		st.HeightSyncCadenceSwallowUntil = savedCadence
		st.HeightSyncSwallowFe = savedSwallowFe
		st.HeightSyncTurnK = savedTurnK
		st.HeightSyncTurnSlots = savedTurnSlots
		st.HeightSyncTurnReason = savedReason
		if snapFloor == nil {
			logging.Warn("heightsync: snapshot restore could not load diffs",
				"escrow_id", st.EscrowID, "error", err)
			tracker.SeedCompleted(savedLast, savedStart)
			return restore(emptyFloor, false, fmt.Errorf("%w: %v", types.ErrFloorNotRestored, err))
		}
		logging.Warn("heightsync: snapshot restore could not load diffs; using snapshot floor",
			"escrow_id", st.EscrowID, "error", err)
	} else if snapFloor == nil {
		return restore(emptyFloor, false, fmt.Errorf("%w: no inference store", types.ErrFloorNotRestored))
	}

	tracker.SeedCompleted(savedLast, savedStart)
	cloned := snapFloor.Clone()
	cloned.ApplyConfig(cfg)
	return restore(cloned, true, nil)
}

// floorClaims attributes each Diff-resident height to the identity that
// signed it, which is what the floor's raise rule counts.
//
// Every carrier is named in the log itself: `slot_id` on an ack (L2 verifies the
// signature over it), `executor_slot` on a finish, the executor of record for a
// confirm — the assignment the state machine made at PrepareInference, so no
// wire field is added — and the sequencer for the legs it composes. A leg the
// log cannot attribute is still judged by L0; it simply does not vote on where
// the floor goes.
func floorClaims(st *types.EscrowState, txs []*types.DevshardTx) []heightsync.FloorClaim {
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
			rec := st.Inferences[tx.GetConfirmStart().InferenceId]
			if rec == nil {
				continue
			}
			signer = rec.ExecutorSlot
		}
		claims = append(claims, heightsync.FloorClaim{Signer: signer, Height: h, Hash: hash})
	}
	return claims
}

// HeightSyncLatestTurnStart is the span-start nonce of the newest turn folded from
// Diff. RecoverSession
// restores the producer counter from this; it is derived and not hashed.
func (sm *StateMachine) HeightSyncLatestTurnStart() uint64 {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.state.HeightSyncLatestTurnStart
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
func (sm *StateMachine) HeightSyncMissingAcks(turnStart uint64) []uint32 {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	if sm.turnTracker == nil {
		return nil
	}
	return sm.turnTracker.MissingAcksDue(turnStart, sm.turnTracker.LastCompletedHeight())
}

// HeightSyncTurnRecord is a copy of the verifier-computed turn, or nil.
func (sm *StateMachine) HeightSyncTurnRecord(turnStart uint64) *heightsync.SyncTurnRecord {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	if sm.turnTracker == nil {
		return nil
	}
	return sm.turnTracker.Record(turnStart)
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
