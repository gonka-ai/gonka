package storage

import (
	"errors"
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

type failingPGStorage struct {
	recordingStorage
	err            error
	hasSessions    bool
	liveHasRows    bool
	liveErr        error
	liveCheckCalls int
}

func (f *failingPGStorage) CreateSession(params CreateSessionParams) error {
	f.lastMethod = "CreateSession"
	return f.err
}

func (f *failingPGStorage) HasAnySessions() bool {
	return f.hasSessions
}

func (f *failingPGStorage) HasAnySessionsLive() (bool, error) {
	f.liveCheckCalls++
	return f.liveHasRows, f.liveErr
}

// failingPGStorageNoLive fails creates but cannot prove emptiness against the
// database. The router must keep .pg-bound conservatively.
type failingPGStorageNoLive struct {
	recordingStorage
	err error
}

func (f *failingPGStorageNoLive) CreateSession(params CreateSessionParams) error {
	f.lastMethod = "CreateSession"
	return f.err
}

func (f *failingPGStorageNoLive) HasAnySessions() bool { return false }

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

func TestHybridStorage_ClearsPGBoundAfterFailedPGCreateWhenProvablyEmpty(t *testing.T) {
	createErr := errors.New("pg insert failed")
	pg := &failingPGStorage{err: createErr}
	storeDir := t.TempDir()
	h := newHybridRouter(nil, pg, true, storeDir)

	err := h.CreateSession(CreateSessionParams{EscrowID: "pg-fail"})
	require.ErrorIs(t, err, createErr)
	require.Equal(t, "CreateSession", pg.lastMethod)
	require.Equal(t, 1, pg.liveCheckCalls, "cleanup must verify emptiness against the DB")

	pgBound, err := ReadPGBound(storeDir)
	require.NoError(t, err)
	require.False(t, pgBound, "stale .pg-bound must be cleared when PG is provably empty")
	require.False(t, h.pgBoundSet)
}

func TestHybridStorage_KeepsPGBoundAfterFailedPGCreateWhenPGHasSessions(t *testing.T) {
	createErr := errors.New("pg insert failed")
	pg := &failingPGStorage{err: createErr, hasSessions: true}
	storeDir := t.TempDir()
	h := newHybridRouter(nil, pg, true, storeDir)

	err := h.CreateSession(CreateSessionParams{EscrowID: "pg-fail"})
	require.ErrorIs(t, err, createErr)
	require.Equal(t, 0, pg.liveCheckCalls, "in-memory sessions already retain the marker")

	pgBound, err := ReadPGBound(storeDir)
	require.NoError(t, err)
	require.True(t, pgBound, ".pg-bound must remain while PG reports sessions")
	require.True(t, h.pgBoundSet)
}

func TestHybridStorage_KeepsPGBoundWhenLiveCheckFailsDuringOutage(t *testing.T) {
	// A create that times out during a PG outage is ambiguous: the insert may
	// have committed server-side. With the live emptiness check also failing,
	// the marker must be kept.
	createErr := errors.New("pg insert timed out")
	pg := &failingPGStorage{err: createErr, liveErr: errors.New("pg unreachable")}
	storeDir := t.TempDir()
	h := newHybridRouter(nil, pg, true, storeDir)

	err := h.CreateSession(CreateSessionParams{EscrowID: "pg-fail"})
	require.ErrorIs(t, err, createErr)
	require.Equal(t, 1, pg.liveCheckCalls)

	pgBound, err := ReadPGBound(storeDir)
	require.NoError(t, err)
	require.True(t, pgBound, ".pg-bound must survive an outage where emptiness cannot be proven")
	require.True(t, h.pgBoundSet)
}

func TestHybridStorage_KeepsPGBoundWhenFailedCreateActuallyCommitted(t *testing.T) {
	// Ack-lost commit: the client saw an error but the DB has the row. The
	// live check sees it and the marker must be kept.
	createErr := errors.New("pg commit ack lost")
	pg := &failingPGStorage{err: createErr, liveHasRows: true}
	storeDir := t.TempDir()
	h := newHybridRouter(nil, pg, true, storeDir)

	err := h.CreateSession(CreateSessionParams{EscrowID: "pg-fail"})
	require.ErrorIs(t, err, createErr)
	require.Equal(t, 1, pg.liveCheckCalls)

	pgBound, err := ReadPGBound(storeDir)
	require.NoError(t, err)
	require.True(t, pgBound, ".pg-bound must remain when the DB actually holds rows")
	require.True(t, h.pgBoundSet)
}

func TestHybridStorage_KeepsPGBoundWhenBackendLacksLiveCheck(t *testing.T) {
	createErr := errors.New("pg insert failed")
	pg := &failingPGStorageNoLive{err: createErr}
	storeDir := t.TempDir()
	h := newHybridRouter(nil, pg, true, storeDir)

	err := h.CreateSession(CreateSessionParams{EscrowID: "pg-fail"})
	require.ErrorIs(t, err, createErr)

	pgBound, err := ReadPGBound(storeDir)
	require.NoError(t, err)
	require.True(t, pgBound, "without a live check the marker must be kept conservatively")
	require.True(t, h.pgBoundSet)
}
