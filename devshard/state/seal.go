package state

import (
	"encoding/json"
	"fmt"
	"slices"

	"devshard/logging"
	"devshard/storage"
	"devshard/types"
)

// stateClockWindowFactor sets how many recent live inferences the deterministic
// state clock scans, as a multiple of the group size N. Concurrency is bounded
// by the number of slots, so N*factor comfortably covers the in-flight window
// whose ConfirmedAt timestamps approximate "now".
const stateClockWindowFactor = 3

func autoSealInterval(cfg types.SessionConfig) uint64 {
	n := cfg.AutoSealEveryNNonces
	if n == 0 {
		return uint64(types.DefaultAutoSealEveryNNonces)
	}
	return uint64(n)
}

func (sm *StateMachine) autoSealIntervalLocked() uint64 {
	return autoSealInterval(sm.state.Config)
}

func shouldAutoSealAtNonce(interval uint64, nonce uint64) bool {
	if interval == 0 {
		return true
	}
	return nonce%interval == 0
}

// AutoSealEveryNNonces returns the compiled default auto-seal sweep interval.
func AutoSealEveryNNonces() uint64 {
	return uint64(types.DefaultAutoSealEveryNNonces)
}

// ShouldAutoSealAtNonce reports whether autoSealLocked runs at nonce using the
// compiled default interval.
func ShouldAutoSealAtNonce(nonce uint64) bool {
	return shouldAutoSealAtNonce(AutoSealEveryNNonces(), nonce)
}

// NextAutoSealNonce returns the smallest nonce >= after+1 that triggers auto-seal
// using the compiled default interval.
func NextAutoSealNonce(after uint64) uint64 {
	return nextAutoSealNonceAtInterval(AutoSealEveryNNonces(), after)
}

func nextAutoSealNonceAtInterval(interval uint64, after uint64) uint64 {
	n := after + 1
	if interval == 0 {
		return n
	}
	if r := n % interval; r != 0 {
		n += interval - r
	}
	return n
}

func cloneCommittedInferenceEntries(src map[uint64][]byte) map[uint64][]byte {
	if len(src) == 0 {
		return make(map[uint64][]byte)
	}
	dst := make(map[uint64][]byte, len(src))
	for id, entry := range src {
		dst[id] = append([]byte(nil), entry...)
	}
	return dst
}

func (sm *StateMachine) hasCommittedInferenceLocked(id uint64) bool {
	_, ok := sm.committedEntries[id]
	return ok
}

func (sm *StateMachine) updateCommittedEntryLocked(id uint64, rec *types.InferenceRecord) error {
	entry, err := marshalInferenceEntry(id, rec)
	if err != nil {
		return err
	}
	sm.committedEntries[id] = entry
	return nil
}

func (sm *StateMachine) rebuildCommittedEntriesLocked() {
	sm.committedEntries = make(map[uint64][]byte, len(sm.state.Inferences))
	for id, rec := range sm.state.Inferences {
		entry, err := marshalInferenceEntry(id, rec)
		if err != nil {
			logging.Error("failed to rebuild committed inference entry",
				"subsystem", "state",
				"inference_id", id,
				"error", err,
			)
			continue
		}
		sm.committedEntries[id] = entry
	}
}

func (sm *StateMachine) hydrateCommittedInferenceLocked(id uint64) (*types.InferenceRecord, error) {
	entry, ok := sm.committedEntries[id]
	if !ok {
		return nil, fmt.Errorf("%w: inference %d", types.ErrInferenceNotFound, id)
	}
	entryID, rec, err := unmarshalInferenceEntry(entry)
	if err != nil {
		return nil, err
	}
	if entryID != id {
		return nil, fmt.Errorf("committed inference mismatch: entry %d, requested %d", entryID, id)
	}
	return rec, nil
}

func (sm *StateMachine) computeStateRootLocked() ([]byte, error) {
	hostStatsHash, err := computeHostStatsHash(sm.state.HostStats)
	if err != nil {
		return nil, err
	}

	acc := sealedAccBytes32(sm.state.SealedAcc)
	restHash, err := ComputeRestHashV2(sm.state.Balance, acc, sm.state.Inferences, sm.state.WarmKeys)
	if err != nil {
		return nil, err
	}

	return ComputeStateRootFromRestHash(hostStatsHash, restHash, sm.state.Fees, sm.state.Phase, sm.state.StateRootAndProtocolVersion), nil
}

func (sm *StateMachine) ExportCommittedEntries() map[uint64][]byte {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return cloneCommittedInferenceEntries(sm.committedEntries)
}

func (sm *StateMachine) RestoreCommittedEntries(entries map[uint64][]byte) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if len(entries) == 0 {
		sm.rebuildCommittedEntriesLocked()
		return
	}
	sm.committedEntries = cloneCommittedInferenceEntries(entries)
	for id, rec := range sm.state.Inferences {
		if err := sm.updateCommittedEntryLocked(id, rec); err != nil {
			logging.Error("failed to refresh live committed entry during restore",
				"subsystem", "state",
				"inference_id", id,
				"error", err,
			)
		}
	}
}

// ExportSealedNonces returns a copy of the per-id seal nonce map for snapshot
// persistence. Only ids no longer in Mutable.Inferences are meaningful here
// (live ids will be re-sealed at their own nonce).
func (sm *StateMachine) ExportSealedNonces() map[uint64]uint64 {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	if len(sm.sealedNonces) == 0 {
		return nil
	}
	out := make(map[uint64]uint64, len(sm.sealedNonces))
	for id, n := range sm.sealedNonces {
		out[id] = n
	}
	return out
}

// RestoreSealedNonces installs a snapshot's per-id seal nonces. Missing ids
// are tolerated by RebuildSealedInferenceIndex (best-effort fallback).
func (sm *StateMachine) RestoreSealedNonces(nonces map[uint64]uint64) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.sealedNonces = make(map[uint64]uint64, len(nonces))
	for id, n := range nonces {
		sm.sealedNonces[id] = n
	}
}

// GetCommittedRecord returns a deep copy of the committed inference entry for
// the given id (live or sealed). It is the canonical accessor for Phase 0
// cold-path readers (and tests) that need the post-seal record state without
// inspecting Mutable.Inferences directly.
func (sm *StateMachine) GetCommittedRecord(id uint64) (types.InferenceRecord, bool) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	if rec, ok := sm.state.Inferences[id]; ok {
		return *rec, true
	}
	rec, err := sm.hydrateCommittedInferenceLocked(id)
	if err != nil {
		return types.InferenceRecord{}, false
	}
	return *rec, true
}

// drainLiveIntoSealedAccLocked seals every record still in state.Inferences,
// in ascending id order, folding each canonical entry into SealedAcc at
// sealNonce. After this returns, state.Inferences is empty under v2
// composition. Caller must hold sm.mu.
//
// Invoked exactly once at the Finalizing -> Settlement phase transition so
// the on-chain v2 settlement payload does not need to carry live inference
// records: rest_hash is then fully determined by sealed_acc + balance + warm
// keys at the moment of settlement.
func (sm *StateMachine) drainLiveIntoSealedAccLocked(sealNonce uint64) error {
	if len(sm.state.Inferences) == 0 {
		return nil
	}

	ids := make([]uint64, 0, len(sm.state.Inferences))
	for id := range sm.state.Inferences {
		ids = append(ids, id)
	}
	slices.Sort(ids)

	if sm.sealedNonces == nil {
		sm.sealedNonces = make(map[uint64]uint64, len(ids))
	}
	cur := sealedAccBytes32(sm.state.SealedAcc)

	for _, id := range ids {
		rec := sm.state.Inferences[id]
		if err := sm.updateCommittedEntryLocked(id, rec); err != nil {
			return fmt.Errorf("drain live inference %d: %w", id, err)
		}
		entry := append([]byte(nil), sm.committedEntries[id]...)
		cur = FoldSealedAccumulator(cur, sealNonce, id, entry)
		sm.sealedNonces[id] = sealNonce
		delete(sm.committedEntries, id)
		if err := sm.upsertInferenceObsLocked(id, sealNonce, rec); err != nil {
			return fmt.Errorf("persist sealed inference %d during drain: %w", id, err)
		}
		delete(sm.state.Inferences, id)
	}
	sm.state.SealedAcc = append([]byte(nil), cur[:]...)
	return nil
}

// sealEligibleStatus reports whether an inference in this status may be folded
// into the sealed accumulator by the deterministic auto-seal sweep: Finished
// (stale-finished tier) or terminal (Validated/Invalidated/TimedOut). Pending,
// Started and Challenged are still in-flight or mid-vote and are not sealable.
func sealEligibleStatus(s types.InferenceStatus) bool {
	switch s {
	case types.StatusFinished, types.StatusValidated, types.StatusInvalidated, types.StatusTimedOut:
		return true
	default:
		return false
	}
}

// terminalAutoSealStatus reports terminal outcomes that may seal as soon as the
// nonce gate clears, without waiting for the state-clock grace. Finished
// (stale-finished) still requires both the nonce and clock gates.
func terminalAutoSealStatus(s types.InferenceStatus) bool {
	switch s {
	case types.StatusValidated, types.StatusInvalidated, types.StatusTimedOut:
		return true
	default:
		return false
	}
}

// StateClockWindow holds the deterministic state-clock value (max ConfirmedAt
// over the tail window) plus min/max extents from that scan for diagnostics.
type StateClockWindow struct {
	Clock          int64
	MinConfirmedAt int64
	MaxConfirmedAt int64
	Known          bool
}

// stateClockLocked derives a deterministic "current time" purely from state:
// the max ConfirmedAt over confirmed live inferences in the latest
// N*stateClockWindowFactor tail (N = group size), plus the min and max
// ConfirmedAt among those confirmed records for diagnostics. Pending and other
// never-confirmed inferences (ConfirmedAt <= 0) are excluded so they do not
// pull window_min_confirmed_at to zero. Returns Known=false when there are no
// live inferences or no confirmed records in the tail window. Caller holds sm.mu.
func (sm *StateMachine) stateClockLocked() StateClockWindow {
	if len(sm.state.Inferences) == 0 {
		return StateClockWindow{}
	}
	window := len(sm.state.Group) * stateClockWindowFactor
	if window <= 0 {
		window = stateClockWindowFactor
	}

	ids := make([]uint64, 0, len(sm.state.Inferences))
	for id := range sm.state.Inferences {
		ids = append(ids, id)
	}
	slices.Sort(ids)

	// Inference id == start nonce, so the highest ids are the most recently
	// started. Scan the tail window once over confirmed records only.
	start := 0
	if len(ids) > window {
		start = len(ids) - window
	}
	var minConfirmed, maxConfirmed int64
	confirmedInWindow := 0
	for _, id := range ids[start:] {
		c := sm.state.Inferences[id].ConfirmedAt
		if c <= 0 {
			continue
		}
		if confirmedInWindow == 0 {
			minConfirmed = c
			maxConfirmed = c
			confirmedInWindow++
			continue
		}
		confirmedInWindow++
		if c < minConfirmed {
			minConfirmed = c
		}
		if c > maxConfirmed {
			maxConfirmed = c
		}
	}
	if confirmedInWindow == 0 {
		return StateClockWindow{}
	}
	return StateClockWindow{
		Clock:          maxConfirmed,
		MinConfirmedAt: minConfirmed,
		MaxConfirmedAt: maxConfirmed,
		Known:          true,
	}
}

// AutoSealStateClock returns the deterministic state-clock window over the
// current live inference tail. Used for mismatch forensics on devshardctl.
func (sm *StateMachine) AutoSealStateClock() StateClockWindow {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.stateClockLocked()
}

// autoSealCandidate is one seal-eligible live inference and how the grace gates
// evaluated at this seal nonce. Emitted in auto-seal info logs on host/user.
type autoSealCandidate struct {
	ID               uint64 `json:"id"`
	Status           uint8  `json:"status"`
	ConfirmedAt      int64  `json:"confirmed_at"`
	NonceGateOK      bool   `json:"nonce_gate_ok"`
	ClockGateSkipped bool   `json:"clock_gate_skipped"`
	ClockGateOK      bool   `json:"clock_gate_ok"`
	GraceRemaining   int64  `json:"grace_remaining_sec,omitempty"`
	Eligible         bool   `json:"eligible"`
}

func (sm *StateMachine) logAutoSealDiagnosticLocked(
	side string,
	sealNonce uint64,
	clockWin StateClockWindow,
	sealGraceNonces uint64,
	graceSeconds int64,
	stateClock int64,
	candidates []autoSealCandidate,
	sealed []uint64,
) {
	if len(candidates) == 0 && len(sealed) == 0 {
		return
	}
	candidatesJSON, err := json.Marshal(candidates)
	if err != nil {
		candidatesJSON = []byte(fmt.Sprintf("marshal error: %v", err))
	}
	args := []any{
		"subsystem", side,
		"diagnostic", "auto_seal",
		"escrow_id", sm.state.EscrowID,
		"seal_nonce", sealNonce,
		"latest_nonce", sm.state.LatestNonce,
		"inference_seal_grace_nonces", sealGraceNonces,
		"inference_seal_grace_seconds", graceSeconds,
		"state_clock_confirmed_at", stateClock,
		"candidates", string(candidatesJSON),
		"sealed_ids", sealed,
		"sealed_count", len(sealed),
		"live_inferences_count", len(sm.state.Inferences),
	}
	if clockWin.Known {
		args = append(args,
			"window_min_confirmed_at", clockWin.MinConfirmedAt,
			"window_max_confirmed_at", clockWin.MaxConfirmedAt,
		)
	}
	logging.Info("auto-seal evaluation", args...)
}

// autoSealLocked folds every live inference that has cleared both seal gates
// into SealedAcc, in ascending id order, and returns the ids it sealed. It is
// the deterministic replacement for the old host-local, wall-clock prune/seal:
// both gates read only state, so the user (composing a diff), the host
// (applying it) and replay all seal the identical set at the identical nonce
// and agree on the post_state_root.
//
// Per live, seal-eligible inference:
//   - nonce gate (always):  sealNonce >= id + InferenceSealGraceNonces   (id == start nonce)
//   - clock gate (Finished only): stateClock - ConfirmedAt >= InferenceSealGraceSeconds
//
// Terminal statuses (Validated/Invalidated/TimedOut) skip the clock gate and
// seal as soon as the nonce gate clears on the diff that made them terminal.
//
// The obs-store write is best-effort (logged, never fatal) so a transient
// storage error on one node cannot diverge the deterministic seal. Caller must
// hold sm.mu and should invoke this only in the Active phase (settlement uses
// drainLiveIntoSealedAccLocked).
func (sm *StateMachine) autoSealLocked(side string, sealNonce uint64) ([]uint64, StateClockWindow, error) {
	if len(sm.state.Inferences) == 0 {
		return nil, StateClockWindow{}, nil
	}
	sealGraceNonces := uint64(sm.state.Config.InferenceSealGraceNonces)
	graceSeconds := int64(sm.state.Config.InferenceSealGraceSeconds)
	clockWin := sm.stateClockLocked()
	stateClock := clockWin.Clock

	// No confirmed records in the tail window — skip sealing until the clock is known.
	if stateClock == 0 {
		return nil, StateClockWindow{}, nil
	}

	var candidates []autoSealCandidate
	var eligible []uint64
	for id, rec := range sm.state.Inferences {
		if !sealEligibleStatus(rec.Status) {
			continue
		}
		candidate := autoSealCandidate{
			ID:          id,
			Status:      uint8(rec.Status),
			ConfirmedAt: rec.ConfirmedAt,
		}
		candidate.NonceGateOK = sealNonce >= id+sealGraceNonces
		if !candidate.NonceGateOK {
			candidates = append(candidates, candidate)
			continue
		}
		if terminalAutoSealStatus(rec.Status) {
			candidate.ClockGateSkipped = true
			candidate.ClockGateOK = true
			candidate.Eligible = true
			candidates = append(candidates, candidate)
			eligible = append(eligible, id)
			continue
		}
		remaining := graceSeconds - (stateClock - rec.ConfirmedAt)
		candidate.GraceRemaining = remaining
		candidate.ClockGateOK = remaining <= 0
		candidate.Eligible = candidate.ClockGateOK
		candidates = append(candidates, candidate)
		if !candidate.ClockGateOK {
			continue
		}
		eligible = append(eligible, id)
	}
	slices.SortFunc(candidates, func(a, b autoSealCandidate) int {
		switch {
		case a.ID < b.ID:
			return -1
		case a.ID > b.ID:
			return 1
		default:
			return 0
		}
	})
	if len(eligible) == 0 {
		sm.logAutoSealDiagnosticLocked(side, sealNonce, clockWin, sealGraceNonces, graceSeconds, stateClock, candidates, nil)
		return nil, clockWin, nil
	}
	slices.Sort(eligible)

	if sm.sealedNonces == nil {
		sm.sealedNonces = make(map[uint64]uint64, len(eligible))
	}
	cur := sealedAccBytes32(sm.state.SealedAcc)
	for _, id := range eligible {
		rec := sm.state.Inferences[id]
		if err := sm.updateCommittedEntryLocked(id, rec); err != nil {
			return nil, clockWin, fmt.Errorf("auto-seal inference %d: %w", id, err)
		}
		entry := append([]byte(nil), sm.committedEntries[id]...)
		cur = FoldSealedAccumulator(cur, sealNonce, id, entry)
		sm.sealedNonces[id] = sealNonce
		delete(sm.committedEntries, id)
		if err := sm.upsertInferenceObsLocked(id, sealNonce, rec); err != nil {
			// Observability only; never block or diverge the deterministic seal.
			logging.Warn("failed to persist sealed inference obs during auto-seal",
				"subsystem", "state",
				"escrow_id", sm.state.EscrowID,
				"inference_id", id,
				"error", err,
			)
		}
		delete(sm.state.Inferences, id)
	}
	sm.state.SealedAcc = append([]byte(nil), cur[:]...)
	sm.logAutoSealDiagnosticLocked(side, sealNonce, clockWin, sealGraceNonces, graceSeconds, stateClock, candidates, eligible)
	return eligible, clockWin, nil
}

func (sm *StateMachine) SealInference(id uint64) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	rec, ok := sm.state.Inferences[id]
	if !ok {
		return nil
	}
	if err := sm.updateCommittedEntryLocked(id, rec); err != nil {
		return err
	}
	entry := append([]byte(nil), sm.committedEntries[id]...)
	sealedNonce := sm.state.LatestNonce
	if sm.sealedNonces == nil {
		sm.sealedNonces = make(map[uint64]uint64)
	}
	sm.sealedNonces[id] = sealedNonce

	cur := sealedAccBytes32(sm.state.SealedAcc)
	cur = FoldSealedAccumulator(cur, sealedNonce, id, entry)
	sm.state.SealedAcc = append([]byte(nil), cur[:]...)
	delete(sm.committedEntries, id)

	if err := sm.upsertInferenceObsLocked(id, sealedNonce, rec); err != nil {
		return err
	}
	delete(sm.state.Inferences, id)
	return nil
}

// LookupSealedInference returns the inference record persisted at seal time
// (observability only; not part of the state root).
func (sm *StateMachine) LookupSealedInference(id uint64) (types.InferenceRecord, bool) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.lookupSealedInferenceLocked(id)
}

// ExportAllInferenceRecords returns live RAM records plus DB-backed snapshots
// for pruned sealed ids. Live records take precedence when both exist.
func (sm *StateMachine) ExportAllInferenceRecords() map[uint64]types.InferenceRecord {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	out := make(map[uint64]types.InferenceRecord, len(sm.state.Inferences)+len(sm.sealedNonces))
	for id, rec := range sm.state.Inferences {
		out[id] = *rec
	}
	for id := range sm.sealedNonces {
		if _, live := out[id]; live {
			continue
		}
		if rec, ok := sm.lookupSealedInferenceLocked(id); ok {
			out[id] = rec
			continue
		}
		if rec, err := sm.hydrateCommittedInferenceLocked(id); err == nil && rec != nil {
			out[id] = *rec
		}
	}
	return out
}

func (sm *StateMachine) lookupSealedInferenceLocked(id uint64) (types.InferenceRecord, bool) {
	row, ok, err := sm.inferenceStore.GetSealedInference(sm.state.EscrowID, id)
	if err != nil || !ok || !row.ObsPresent {
		return types.InferenceRecord{}, false
	}
	return inferenceRecordFromObsRow(row), true
}

func (sm *StateMachine) RebuildSealedInferenceIndex() error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if err := sm.inferenceStore.DeleteSealedInferences(sm.state.EscrowID); err != nil {
		return err
	}
	ids := make([]uint64, 0, len(sm.sealedNonces))
	for id := range sm.sealedNonces {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		if _, live := sm.state.Inferences[id]; live {
			continue
		}
		nonce, ok := sm.sealedNonces[id]
		if !ok {
			nonce = sm.state.LatestNonce
		}
		row := storage.InferenceRow{InferenceID: id, SealedNonce: nonce}
		if cached, ok := sm.committedEntries[id]; ok {
			if entryID, rec, err := unmarshalInferenceEntry(cached); err == nil && entryID == id {
				row = inferenceObsRow(id, nonce, rec)
			}
		}
		if err := sm.inferenceStore.InsertSealedInference(sm.state.EscrowID, row); err != nil {
			return err
		}
	}
	return nil
}

// persistLiveInferenceObsLocked upserts the current live inference snapshot
// (e.g. on StatusChallenged) before RAM prune. Caller must hold sm.mu.
func (sm *StateMachine) persistLiveInferenceObsLocked(id uint64, rec *types.InferenceRecord) error {
	sealNonce, _ := sm.sealedNonces[id]
	return sm.upsertInferenceObsLocked(id, sealNonce, rec)
}

// persistLiveInferenceObsBestEffortLocked upserts live inference obs for
// observability only. Storage errors are logged and never fail the tx caller,
// matching the auto-seal contract (recovery rebuilds from the diff journal).
func (sm *StateMachine) persistLiveInferenceObsBestEffortLocked(id uint64, rec *types.InferenceRecord) {
	if err := sm.persistLiveInferenceObsLocked(id, rec); err != nil {
		logging.Warn("failed to persist live inference obs; continuing (best-effort, recovery rebuilds from diffs)",
			"subsystem", "state",
			"escrow_id", sm.state.EscrowID,
			"inference_id", id,
			"error", err,
		)
	}
}

// upsertInferenceObsLocked writes or updates the observability row for an inference.
// On seal, DrainInferenceValidationObs moves live validation counters into sealed storage.
// Caller must hold sm.mu.
func (sm *StateMachine) upsertInferenceObsLocked(id, sealedNonce uint64, rec *types.InferenceRecord) error {
	if err := sm.inferenceStore.InsertSealedInference(sm.state.EscrowID, inferenceObsRow(id, sealedNonce, rec)); err != nil {
		return err
	}
	if sealedNonce > 0 {
		if err := sm.inferenceStore.DrainInferenceValidationObs(sm.state.EscrowID, id); err != nil {
			return fmt.Errorf("drain validation obs: %w", err)
		}
	}
	return nil
}

func inferenceObsRow(id, sealedNonce uint64, rec *types.InferenceRecord) storage.InferenceRow {
	row := storage.InferenceRow{
		InferenceID:        id,
		SealedNonce:        sealedNonce,
		ObsPresent:         true,
		SealedStatus:       uint32(rec.Status),
		SealedExecutorSlot: rec.ExecutorSlot,
		SealedVotesValid:   rec.VotesValid,
		SealedVotesInvalid: rec.VotesInvalid,
		SealedModel:        rec.Model,
		SealedInputLength:  rec.InputLength,
		SealedMaxTokens:    rec.MaxTokens,
		SealedInputTokens:  rec.InputTokens,
		SealedOutputTokens: rec.OutputTokens,
		SealedReservedCost: rec.ReservedCost,
		SealedActualCost:   rec.ActualCost,
		SealedStartedAt:    rec.StartedAt,
		SealedConfirmedAt:  rec.ConfirmedAt,
	}
	if len(rec.PromptHash) > 0 {
		row.SealedPromptHash = append([]byte(nil), rec.PromptHash...)
	}
	if len(rec.ResponseHash) > 0 {
		row.SealedResponseHash = append([]byte(nil), rec.ResponseHash...)
	}
	if vb := rec.ValidatedBy.Bytes(); len(vb) > 0 {
		row.SealedValidatedBy = append([]byte(nil), vb...)
	}
	return row
}

func inferenceRecordFromObsRow(row storage.InferenceRow) types.InferenceRecord {
	return types.InferenceRecord{
		Status:       types.InferenceStatus(row.SealedStatus),
		ExecutorSlot: row.SealedExecutorSlot,
		Model:        row.SealedModel,
		PromptHash:   append([]byte(nil), row.SealedPromptHash...),
		ResponseHash: append([]byte(nil), row.SealedResponseHash...),
		InputLength:  row.SealedInputLength,
		MaxTokens:    row.SealedMaxTokens,
		InputTokens:  row.SealedInputTokens,
		OutputTokens: row.SealedOutputTokens,
		ReservedCost: row.SealedReservedCost,
		ActualCost:   row.SealedActualCost,
		StartedAt:    row.SealedStartedAt,
		ConfirmedAt:  row.SealedConfirmedAt,
		VotesValid:   row.SealedVotesValid,
		VotesInvalid: row.SealedVotesInvalid,
		ValidatedBy:  types.Bitmap128FromBytes(row.SealedValidatedBy),
	}
}
