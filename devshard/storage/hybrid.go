package storage

import (
	"fmt"

	"devshard/types"
)

// HybridStorage forwards every Storage call to a single backend chosen at
// process startup by NewStorage. It exists so ManagedStorage and callers keep
// a stable wrapper type without dual-backend routing.
type HybridStorage struct {
	backend Storage
}

// NewHybridStorage wraps the backend selected at boot.
func NewHybridStorage(backend Storage) *HybridStorage {
	return &HybridStorage{backend: backend}
}

func (h *HybridStorage) CreateSession(params CreateSessionParams) error {
	return h.backend.CreateSession(params)
}

func (h *HybridStorage) MarkSettled(escrowID string) error {
	return h.backend.MarkSettled(escrowID)
}

func (h *HybridStorage) ListActiveSessions() ([]ActiveSession, error) {
	return h.backend.ListActiveSessions()
}

func (h *HybridStorage) AppendDiff(escrowID string, rec types.DiffRecord) error {
	return h.backend.AppendDiff(escrowID, rec)
}

func (h *HybridStorage) GetDiffs(escrowID string, fromNonce, toNonce uint64) ([]types.DiffRecord, error) {
	return h.backend.GetDiffs(escrowID, fromNonce, toNonce)
}

func (h *HybridStorage) AddSignature(escrowID string, nonce uint64, slotID uint32, sig []byte) error {
	return h.backend.AddSignature(escrowID, nonce, slotID, sig)
}

func (h *HybridStorage) GetSignatures(escrowID string, nonce uint64) (map[uint32][]byte, error) {
	return h.backend.GetSignatures(escrowID, nonce)
}

func (h *HybridStorage) GetSessionMeta(escrowID string) (*SessionMeta, error) {
	return h.backend.GetSessionMeta(escrowID)
}

func (h *HybridStorage) MarkFinalized(escrowID string, nonce uint64) error {
	return h.backend.MarkFinalized(escrowID, nonce)
}

func (h *HybridStorage) LastFinalized(escrowID string) (uint64, error) {
	return h.backend.LastFinalized(escrowID)
}

func (h *HybridStorage) SaveSnapshot(escrowID string, nonce uint64, data []byte) error {
	return h.backend.SaveSnapshot(escrowID, nonce, data)
}

func (h *HybridStorage) LoadSnapshot(escrowID string) (uint64, []byte, error) {
	return h.backend.LoadSnapshot(escrowID)
}

func (h *HybridStorage) InsertSealedInference(escrowID string, row InferenceRow) error {
	return h.backend.InsertSealedInference(escrowID, row)
}

func (h *HybridStorage) GetSealedInference(escrowID string, inferenceID uint64) (InferenceRow, bool, error) {
	return h.backend.GetSealedInference(escrowID, inferenceID)
}

func (h *HybridStorage) DeleteSealedInferences(escrowID string) error {
	return h.backend.DeleteSealedInferences(escrowID)
}

func (h *HybridStorage) RecordValidationsAppliedOnce(escrowID string, entries []ValidationObsEntry) error {
	return h.backend.RecordValidationsAppliedOnce(escrowID, entries)
}

func (h *HybridStorage) DrainInferenceValidationObs(escrowID string, inferenceID uint64) error {
	return h.backend.DrainInferenceValidationObs(escrowID, inferenceID)
}

func (h *HybridStorage) GetValidationObservability(escrowID string) ([]SlotValidationObs, error) {
	return h.backend.GetValidationObservability(escrowID)
}

func (h *HybridStorage) PruneEpoch(epochID uint64) error {
	return h.backend.PruneEpoch(epochID)
}

func (h *HybridStorage) pruneBefore(cutoff uint64) error {
	rp, ok := h.backend.(rangePruner)
	if !ok {
		return fmt.Errorf("storage backend does not support range prune")
	}
	return rp.pruneBefore(cutoff)
}

func (h *HybridStorage) Close() error {
	return h.backend.Close()
}

var _ Storage = (*HybridStorage)(nil)
