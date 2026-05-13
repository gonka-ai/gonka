package state

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"slices"

	"devshard/logging"
	"devshard/storage"
	"devshard/types"
)

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

	var restHash []byte
	if sm.effectiveV2Composition() {
		acc := sealedAccBytes32(sm.state.SealedAcc)
		restHash, err = ComputeRestHashV2(sm.state.Balance, acc, sm.state.Inferences, sm.state.WarmKeys)
		if err != nil {
			return nil, err
		}
	} else {
		infHash := computeInferencesHashFromEntries(sm.committedEntries)
		warmKeysHash := computeWarmKeysHash(sm.state.WarmKeys)

		balBytes := make([]byte, 8)
		binary.BigEndian.PutUint64(balBytes, sm.state.Balance)

		h := sha256.New()
		h.Write(balBytes)
		h.Write(infHash)
		h.Write(warmKeysHash)
		restHash = h.Sum(nil)
	}

	return ComputeStateRootFromRestHash(hostStatsHash, restHash, sm.state.Fees, sm.state.Phase, sm.state.Version), nil
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
//
// On v1 composition this is a no-op (live inferences remain in the map and
// contribute to the v1 inferences hash directly).
func (sm *StateMachine) drainLiveIntoSealedAccLocked(sealNonce uint64) error {
	if !sm.effectiveV2Composition() {
		return nil
	}
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
		if sm.inferenceStore != nil {
			if err := sm.inferenceStore.InsertSealedInference(sm.state.EscrowID, inferenceRow(id, sealNonce)); err != nil {
				// Storage is auxiliary; sealed_acc is the canonical commitment.
				// Mirror SealInference's behaviour and surface the error so the
				// caller can roll back via mutableSnapshot.
				return fmt.Errorf("persist sealed inference %d during drain: %w", id, err)
			}
		}
		delete(sm.state.Inferences, id)
	}
	sm.state.SealedAcc = append([]byte(nil), cur[:]...)
	return nil
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

	if sm.effectiveV2Composition() {
		cur := sealedAccBytes32(sm.state.SealedAcc)
		cur = FoldSealedAccumulator(cur, sealedNonce, id, entry)
		sm.state.SealedAcc = append([]byte(nil), cur[:]...)
		delete(sm.committedEntries, id)
	}

	if sm.inferenceStore != nil {
		if err := sm.inferenceStore.InsertSealedInference(sm.state.EscrowID, inferenceRow(id, sealedNonce)); err != nil {
			return err
		}
	}
	delete(sm.state.Inferences, id)
	return nil
}

func (sm *StateMachine) RebuildSealedInferenceIndex() error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if sm.inferenceStore == nil {
		return nil
	}
	if err := sm.inferenceStore.DeleteSealedInferences(sm.state.EscrowID); err != nil {
		return err
	}
	if sm.effectiveV2Composition() {
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
			if err := sm.inferenceStore.InsertSealedInference(sm.state.EscrowID, inferenceRow(id, nonce)); err != nil {
				return err
			}
		}
		return nil
	}
	for id := range sm.committedEntries {
		if _, live := sm.state.Inferences[id]; live {
			continue
		}
		nonce, ok := sm.sealedNonces[id]
		if !ok {
			// We have an entry sealed before we started tracking nonces (e.g. a
			// snapshot from a build that did not persist sealed_nonces). Use the
			// current latest nonce as a best-effort marker; the row only signals
			// "this id was sealed", the precise seal nonce is purely audit data.
			nonce = sm.state.LatestNonce
		}
		if err := sm.inferenceStore.InsertSealedInference(sm.state.EscrowID, inferenceRow(id, nonce)); err != nil {
			return err
		}
	}
	return nil
}

func inferenceRow(id, sealedNonce uint64) storage.InferenceRow {
	return storage.InferenceRow{
		InferenceID: id,
		SealedNonce: sealedNonce,
	}
}
