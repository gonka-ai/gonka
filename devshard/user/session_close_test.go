package user

import (
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/storage"
	"devshard/types"
)

// closeCountingStore is a storage.Storage that records how many times Close is
// called. Every other method is an inert stub: this fake exists only to prove
// that Session.Close releases the underlying store, which is the resource the
// per-runtime memory leak was failing to free.
type closeCountingStore struct {
	closeCalls int
}

func (s *closeCountingStore) CreateSession(storage.CreateSessionParams) error { return nil }
func (s *closeCountingStore) MarkSettled(string) error                        { return nil }
func (s *closeCountingStore) ListActiveSessions() ([]storage.ActiveSession, error) {
	return nil, nil
}
func (s *closeCountingStore) AppendDiff(string, types.DiffRecord) error { return nil }
func (s *closeCountingStore) GetDiffs(string, uint64, uint64) ([]types.DiffRecord, error) {
	return nil, nil
}
func (s *closeCountingStore) AddSignature(string, uint64, uint32, []byte) error { return nil }
func (s *closeCountingStore) GetSignatures(string, uint64) (map[uint32][]byte, error) {
	return nil, nil
}
func (s *closeCountingStore) GetSessionMeta(string) (*storage.SessionMeta, error) {
	return nil, storage.ErrSessionNotFound
}
func (s *closeCountingStore) MarkFinalized(string, uint64) error        { return nil }
func (s *closeCountingStore) LastFinalized(string) (uint64, error)      { return 0, nil }
func (s *closeCountingStore) SaveSnapshot(string, uint64, []byte) error { return nil }
func (s *closeCountingStore) LoadSnapshot(string) (uint64, []byte, error) {
	return 0, nil, storage.ErrSnapshotNotFound
}
func (s *closeCountingStore) InsertSealedInference(string, storage.InferenceRow) error { return nil }
func (s *closeCountingStore) GetSealedInference(string, uint64) (storage.InferenceRow, bool, error) {
	return storage.InferenceRow{}, false, nil
}
func (s *closeCountingStore) DeleteSealedInferences(string) error { return nil }
func (s *closeCountingStore) RecordValidationsAppliedOnce(string, []storage.ValidationObsEntry) error {
	return nil
}
func (s *closeCountingStore) DrainInferenceValidationObs(string, uint64) error { return nil }
func (s *closeCountingStore) GetValidationObservability(string) ([]storage.SlotValidationObs, error) {
	return nil, nil
}
func (s *closeCountingStore) ClearValidationObs(string) error { return nil }
func (s *closeCountingStore) PruneEpoch(uint64) error         { return nil }
func (s *closeCountingStore) Close() error {
	s.closeCalls++
	return nil
}

// TestSession_Close_ClosesUnderlyingStore proves the resource-release leg of the
// leak fix: closing a Session must close the storage it owns. The gateway-side
// tests prove rt.close() is now invoked on every automatic deactivation path;
// this proves that invocation actually frees the SQLite store the session holds.
func TestSession_Close_ClosesUnderlyingStore(t *testing.T) {
	store := &closeCountingStore{}
	session, _, _ := setupSessionWithOptions(t, 1, 1_000_000, 0, WithStorage(store))

	require.NoError(t, session.Close())
	require.Equal(t, 1, store.closeCalls, "Session.Close must close the injected storage exactly once")
}
