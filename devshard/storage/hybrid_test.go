package storage

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/types"
)

type recordingStorage struct {
	lastMethod     string
	activeSessions []ActiveSession
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
	return r.activeSessions, nil
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
func (r *recordingStorage) ClearValidationObs(escrowID string) error {
	r.lastMethod = "ClearValidationObs"
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

type owningRecordingStorage struct {
	recordingStorage
	owned map[string]struct{}
}

func (o *owningRecordingStorage) HasEscrow(escrowID string) bool {
	_, ok := o.owned[escrowID]
	return ok
}

func (o *owningRecordingStorage) EscrowIDs() []string {
	ids := make([]string, 0, len(o.owned))
	for id := range o.owned {
		ids = append(ids, id)
	}
	return ids
}

func (o *owningRecordingStorage) CreateSession(params CreateSessionParams) error {
	o.lastMethod = "CreateSession"
	if o.owned == nil {
		o.owned = make(map[string]struct{})
	}
	o.owned[params.EscrowID] = struct{}{}
	return nil
}

func TestHybridStorage_forwardsStorageMethods(t *testing.T) {
	rec := &recordingStorage{}
	h := newHybridRouter(rec, nil, false, "")

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

func TestHybridStorage_ReconnectPromotesDegradedRouter(t *testing.T) {
	sqlite := &owningRecordingStorage{owned: map[string]struct{}{"sqlite-owned": {}}}
	h := newDegradedSQLiteRouter(sqlite, t.TempDir(), ErrStoragePostgresUnavailable)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pg := &recordingStorage{}
	attempts := 0
	h.startPostgresReconnect(ctx, func(context.Context) (Storage, error) {
		attempts++
		if attempts == 1 {
			return nil, errors.New("pg down")
		}
		return pg, nil
	}, 5*time.Millisecond)

	require.Eventually(t, func() bool {
		h.mu.RLock()
		degraded := h.degradedOwnerOnly
		attached := h.pg == pg
		h.mu.RUnlock()
		return attached && !degraded
	}, time.Second, 10*time.Millisecond)

	require.NoError(t, h.CreateSession(CreateSessionParams{EscrowID: "new-pg"}))
	require.Equal(t, "CreateSession", pg.lastMethod, "new sessions must use PG after promotion")
}

func TestHybridStorage_PromoteClosesIncomingBackendWhenAlreadyPromoted(t *testing.T) {
	existing := &recordingStorage{}
	incoming := &recordingStorage{}
	h := newHybridRouter(nil, existing, true, "")

	require.NoError(t, h.promotePostgres(incoming))
	require.Same(t, existing, h.pg)
	require.Equal(t, "Close", incoming.lastMethod)
}

func TestHybridStorage_PromotionHookFiresAfterReconnectAndImmediatelyAfterPromotion(t *testing.T) {
	h := newDegradedSQLiteRouter(nil, t.TempDir(), ErrStoragePostgresUnavailable)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	first := make(chan struct{}, 1)
	h.OnPostgresPromoted(func() { first <- struct{}{} })
	h.startPostgresReconnect(ctx, func(context.Context) (Storage, error) {
		return &recordingStorage{}, nil
	}, 5*time.Millisecond)

	require.Eventually(t, func() bool {
		select {
		case <-first:
			return true
		default:
			return false
		}
	}, time.Second, 10*time.Millisecond)

	second := make(chan struct{}, 1)
	h.OnPostgresPromoted(func() { second <- struct{}{} })
	require.Eventually(t, func() bool {
		select {
		case <-second:
			return true
		default:
			return false
		}
	}, time.Second, 10*time.Millisecond)
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

func TestHybridStorage_PruneKeepsPGBoundWhenLiveCheckFindsRows(t *testing.T) {
	pg := &failingPGStorage{liveHasRows: true}
	storeDir := t.TempDir()
	require.NoError(t, WritePGBound(storeDir))
	h := newHybridRouter(nil, pg, true, storeDir)
	h.pgBoundSet = true

	require.NoError(t, h.PruneEpoch(10))
	require.Equal(t, 1, pg.liveCheckCalls, "prune cleanup must verify emptiness against the DB")

	pgBound, err := ReadPGBound(storeDir)
	require.NoError(t, err)
	require.True(t, pgBound, ".pg-bound must remain when live PG rows exist")
	require.True(t, h.pgBoundSet)
}

func TestHybridStorage_QuarantinesDuplicateBackendOwnership(t *testing.T) {
	sqlite := &owningRecordingStorage{owned: map[string]struct{}{
		"dupe":        {},
		"sqlite-only": {},
	}}
	pg := &owningRecordingStorage{owned: map[string]struct{}{
		"dupe":    {},
		"pg-only": {},
	}}
	h := newHybridRouter(sqlite, pg, true, "")

	require.ElementsMatch(t, []string{"dupe"}, h.conflictedEscrowIDs())
	logs := captureStorageLogs(t)
	h.logConflictedEscrows("test")
	entry := requireStorageLogEntry(t, readStorageLogEntries(t, logs),
		"devshard storage: escrow exists in both sqlite and postgres; quarantining conflicted escrows")
	require.Equal(t, "test", entry["phase"])

	err := h.CreateSession(CreateSessionParams{EscrowID: "dupe"})
	require.ErrorIs(t, err, ErrEscrowBackendConflict)
	_, err = h.GetSessionMeta("dupe")
	require.ErrorIs(t, err, ErrEscrowBackendConflict)

	require.NoError(t, h.MarkSettled("sqlite-only"))
	require.Equal(t, "MarkSettled", sqlite.lastMethod)
	require.Empty(t, pg.lastMethod)

	require.NoError(t, h.MarkSettled("pg-only"))
	require.Equal(t, "MarkSettled", pg.lastMethod)
}

func TestHybridStorage_ListActiveSessionsDedupesEscrowIDs(t *testing.T) {
	sqlite := &recordingStorage{activeSessions: []ActiveSession{
		{EscrowID: "dupe", EpochID: 9},
		{EscrowID: "sqlite-only", EpochID: 9},
	}}
	pg := &recordingStorage{activeSessions: []ActiveSession{
		{EscrowID: "dupe", EpochID: 10},
		{EscrowID: "pg-only", EpochID: 10},
	}}
	h := newHybridRouter(sqlite, pg, true, "")

	sessions, err := h.ListActiveSessions()
	require.NoError(t, err)
	require.Equal(t, []ActiveSession{
		{EscrowID: "dupe", EpochID: 9},
		{EscrowID: "sqlite-only", EpochID: 9},
		{EscrowID: "pg-only", EpochID: 10},
	}, sessions)
}
