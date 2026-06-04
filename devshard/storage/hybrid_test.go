package storage

import (
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/types"
)

type recordingStorage struct {
	lastMethod string
}

func (r *recordingStorage) CreateSession(params CreateSessionParams) error {
	r.lastMethod = "CreateSession"
	return nil
}
func (r *recordingStorage) MarkSettled(escrowID string) error {
	r.lastMethod = "MarkSettled"
	return nil
}
func (r *recordingStorage) ListActiveSessions() ([]ActiveSession, error) {
	r.lastMethod = "ListActiveSessions"
	return nil, nil
}
func (r *recordingStorage) AppendDiff(escrowID string, rec types.DiffRecord) error {
	r.lastMethod = "AppendDiff"
	return nil
}
func (r *recordingStorage) GetDiffs(escrowID string, fromNonce, toNonce uint64) ([]types.DiffRecord, error) {
	r.lastMethod = "GetDiffs"
	return nil, nil
}
func (r *recordingStorage) AddSignature(escrowID string, nonce uint64, slotID uint32, sig []byte) error {
	r.lastMethod = "AddSignature"
	return nil
}
func (r *recordingStorage) GetSignatures(escrowID string, nonce uint64) (map[uint32][]byte, error) {
	r.lastMethod = "GetSignatures"
	return nil, nil
}
func (r *recordingStorage) GetSessionMeta(escrowID string) (*SessionMeta, error) {
	r.lastMethod = "GetSessionMeta"
	return nil, ErrSessionNotFound
}
func (r *recordingStorage) MarkFinalized(escrowID string, nonce uint64) error {
	r.lastMethod = "MarkFinalized"
	return nil
}
func (r *recordingStorage) LastFinalized(escrowID string) (uint64, error) {
	r.lastMethod = "LastFinalized"
	return 0, nil
}
func (r *recordingStorage) SaveSnapshot(escrowID string, nonce uint64, data []byte) error {
	r.lastMethod = "SaveSnapshot"
	return nil
}
func (r *recordingStorage) LoadSnapshot(escrowID string) (uint64, []byte, error) {
	r.lastMethod = "LoadSnapshot"
	return 0, nil, ErrSnapshotNotFound
}
func (r *recordingStorage) InsertSealedInference(escrowID string, row InferenceRow) error {
	r.lastMethod = "InsertSealedInference"
	return nil
}
func (r *recordingStorage) GetSealedInference(escrowID string, inferenceID uint64) (InferenceRow, bool, error) {
	r.lastMethod = "GetSealedInference"
	return InferenceRow{}, false, nil
}
func (r *recordingStorage) DeleteSealedInferences(escrowID string) error {
	r.lastMethod = "DeleteSealedInferences"
	return nil
}
func (r *recordingStorage) RecordValidationsAppliedOnce(escrowID string, entries []ValidationObsEntry) error {
	r.lastMethod = "RecordValidationsAppliedOnce"
	return nil
}
func (r *recordingStorage) DrainInferenceValidationObs(escrowID string, inferenceID uint64) error {
	r.lastMethod = "DrainInferenceValidationObs"
	return nil
}
func (r *recordingStorage) GetValidationObservability(escrowID string) ([]SlotValidationObs, error) {
	r.lastMethod = "GetValidationObservability"
	return nil, nil
}
func (r *recordingStorage) PruneEpoch(epochID uint64) error {
	r.lastMethod = "PruneEpoch"
	return nil
}
func (r *recordingStorage) pruneBefore(cutoff uint64) error {
	r.lastMethod = "pruneBefore"
	return nil
}
func (r *recordingStorage) Close() error {
	r.lastMethod = "Close"
	return nil
}

func TestHybridStorage_forwardsStorageMethods(t *testing.T) {
	rec := &recordingStorage{}
	h := NewHybridStorage(rec)

	require.NoError(t, h.CreateSession(CreateSessionParams{EscrowID: "e"}))
	require.Equal(t, "CreateSession", rec.lastMethod)

	require.NoError(t, h.MarkSettled("e"))
	require.Equal(t, "MarkSettled", rec.lastMethod)

	_, err := h.ListActiveSessions()
	require.NoError(t, err)
	require.Equal(t, "ListActiveSessions", rec.lastMethod)

	require.NoError(t, h.AppendDiff("e", types.DiffRecord{}))
	require.Equal(t, "AppendDiff", rec.lastMethod)

	_, err = h.GetDiffs("e", 0, 1)
	require.NoError(t, err)
	require.Equal(t, "GetDiffs", rec.lastMethod)

	require.NoError(t, h.AddSignature("e", 1, 0, nil))
	require.Equal(t, "AddSignature", rec.lastMethod)

	_, err = h.GetSignatures("e", 1)
	require.NoError(t, err)
	require.Equal(t, "GetSignatures", rec.lastMethod)

	_, err = h.GetSessionMeta("e")
	require.ErrorIs(t, err, ErrSessionNotFound)
	require.Equal(t, "GetSessionMeta", rec.lastMethod)

	require.NoError(t, h.MarkFinalized("e", 1))
	require.Equal(t, "MarkFinalized", rec.lastMethod)

	_, err = h.LastFinalized("e")
	require.NoError(t, err)
	require.Equal(t, "LastFinalized", rec.lastMethod)

	require.NoError(t, h.SaveSnapshot("e", 1, []byte("x")))
	require.Equal(t, "SaveSnapshot", rec.lastMethod)

	_, _, err = h.LoadSnapshot("e")
	require.ErrorIs(t, err, ErrSnapshotNotFound)
	require.Equal(t, "LoadSnapshot", rec.lastMethod)

	require.NoError(t, h.InsertSealedInference("e", InferenceRow{}))
	require.Equal(t, "InsertSealedInference", rec.lastMethod)

	_, _, err = h.GetSealedInference("e", 1)
	require.NoError(t, err)
	require.Equal(t, "GetSealedInference", rec.lastMethod)

	require.NoError(t, h.DeleteSealedInferences("e"))
	require.Equal(t, "DeleteSealedInferences", rec.lastMethod)

	require.NoError(t, h.PruneEpoch(1))
	require.Equal(t, "PruneEpoch", rec.lastMethod)

	require.NoError(t, h.pruneBefore(2))
	require.Equal(t, "pruneBefore", rec.lastMethod)

	require.NoError(t, h.Close())
	require.Equal(t, "Close", rec.lastMethod)
}
