package user

import (
	"context"
	"testing"

	"devshard/host"
	"devshard/internal/testutil"
	"devshard/storage"
	"devshard/types"

	"github.com/stretchr/testify/require"
)

func sessionOnSQLite(t *testing.T) (*Session, *storage.SQLite) {
	t.Helper()
	s, _, creator := setupSession(t, 3, 100000, 10)
	db := newTestStore(t)
	require.NoError(t, db.CreateSession(storage.CreateSessionParams{EscrowID: "escrow-1", Version: testutil.RuntimeTestVersion, CreatorAddr: creator.Address(), Config: s.sm.SnapshotState().Config, Group: s.group, InitialBalance: 100000}))
	s.store = db
	return s, db
}

func journalNonces(t *testing.T, db *storage.SQLite) []uint64 {
	t.Helper()
	rows, err := db.ListCompletions("escrow-1")
	require.NoError(t, err)
	var nonces []uint64
	for _, row := range rows {
		nonces = append(nonces, row.Nonce)
	}
	return nonces
}

func TestSessionJournalHoldsAStartUntilTheDiffThatResolvesIt(t *testing.T) {
	s, db := sessionOnSQLite(t)
	legacy := newMemoryCompletionStore()
	s.UseSessionCompletionJournal(legacy)
	_, err := s.SendInference(context.Background(), completionParams())
	require.NoError(t, err)
	require.Equal(t, []uint64{1}, journalNonces(t, db))
	require.NoError(t, s.SendPendingDiff(context.Background()))
	rec, ok := s.sm.Inference(1)
	require.True(t, ok)
	require.True(t, finishAppliedStatus(rec.Status))
	require.Empty(t, journalNonces(t, db))
	entries, err := legacy.ListInferenceCompletions("escrow-1")
	require.NoError(t, err)
	require.Empty(t, entries)
}

func TestSessionJournalTakesOverTheLegacyRows(t *testing.T) {
	s, db := sessionOnSQLite(t)
	legacy := newMemoryCompletionStore()
	require.NoError(t, legacy.SaveInferenceCompletion("escrow-1", InferenceCompletion{Nonce: 7, Payload: host.InferencePayload{Model: "llama"}}))
	s.UseSessionCompletionJournal(legacy)
	require.Equal(t, []uint64{7}, journalNonces(t, db))
	entries, err := legacy.ListInferenceCompletions("escrow-1")
	require.NoError(t, err)
	require.Empty(t, entries)

	// A move that stopped after the copy runs again without a conflict.
	require.NoError(t, legacy.SaveInferenceCompletion("escrow-1", InferenceCompletion{Nonce: 7, Payload: host.InferencePayload{Model: "llama"}}))
	s.UseSessionCompletionJournal(legacy)
	require.Equal(t, []uint64{7}, journalNonces(t, db))
	_, err = s.PrepareInference(context.Background(), completionParams())
	require.NoError(t, err)
}

func TestSessionJournalRefusesStartsWhenTheLegacyRowsCannotMove(t *testing.T) {
	s, _ := sessionOnSQLite(t)
	legacy := newMemoryCompletionStore()
	require.NoError(t, legacy.SaveInferenceCompletion("escrow-1", InferenceCompletion{Nonce: 1, Payload: host.InferencePayload{Model: "llama"}}))
	legacy.deleteErr = context.DeadlineExceeded
	s.UseSessionCompletionJournal(legacy)
	_, err := s.PrepareInference(context.Background(), completionParams())
	require.ErrorContains(t, err, "move completion journal")
	require.Zero(t, s.Nonce())
	require.ErrorIs(t, s.Finalize(context.Background()), ErrInferenceCompletionPending)
}

func TestSessionJournalReplacesARowNothingWasDispatchedUnder(t *testing.T) {
	s, db := sessionOnSQLite(t)
	legacy := newMemoryCompletionStore()
	require.NoError(t, legacy.SaveInferenceCompletion("escrow-1", InferenceCompletion{Nonce: 1, Payload: host.InferencePayload{Model: "orphan"}}))
	s.UseSessionCompletionJournal(legacy)
	_, err := s.PrepareInference(context.Background(), completionParams())
	require.NoError(t, err)
	entries, err := sessionCompletionJournal{journal: db}.ListInferenceCompletions("escrow-1")
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, "llama", entries[0].Payload.Model)
}

func TestSessionJournalKeepsTheObligationOfADurableStart(t *testing.T) {
	s, db := sessionOnSQLite(t)
	s.UseSessionCompletionJournal(nil)
	_, err := s.PrepareInference(context.Background(), completionParams())
	require.NoError(t, err)
	fresh, _, _ := setupSession(t, 3, 100000, 10)
	fresh.store = db
	fresh.UseSessionCompletionJournal(nil)
	require.ErrorIs(t, fresh.Finalize(context.Background()), ErrInferenceCompletionPending)
	params := completionParams()
	params.Model = "replacement"
	_, err = fresh.PrepareInference(context.Background(), params)
	require.ErrorIs(t, err, ErrInferenceCompletionConflict)
	entries, err := sessionCompletionJournal{journal: db}.ListInferenceCompletions("escrow-1")
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, "llama", entries[0].Payload.Model)
}

type failingDiffJournal struct {
	*storage.SQLite
}

func (failingDiffJournal) AppendDiffWithCompletions(string, types.DiffRecord, storage.CompletionWrite) error {
	return storage.ErrDiffFork
}

func TestSessionJournalFailedStartLeavesNoObligation(t *testing.T) {
	s, db := sessionOnSQLite(t)
	s.store = failingDiffJournal{db}
	s.UseSessionCompletionJournal(nil)
	_, err := s.PrepareInference(context.Background(), completionParams())
	require.ErrorIs(t, err, storage.ErrDiffFork)
	require.Zero(t, s.Nonce())
	require.Empty(t, journalNonces(t, db))
	s.mu.Lock()
	staged := s.stagedCompletion
	s.mu.Unlock()
	require.Nil(t, staged)
}
