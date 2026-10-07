package user

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"devshard/internal/testutil"
	"devshard/storage"
	"devshard/types"

	"github.com/stretchr/testify/require"
)

func restartWithCompletionOrphan(t *testing.T) (*Session, *memoryCompletionStore) {
	t.Helper()
	s, _, creator := setupSession(t, 3, 100000, 10)
	path := filepath.Join(t.TempDir(), "session")
	db, err := storage.NewSQLite(path)
	require.NoError(t, err)
	require.NoError(t, db.CreateSession(storage.CreateSessionParams{EscrowID: "escrow-1", Version: testutil.RuntimeTestVersion, CreatorAddr: creator.Address(), Config: s.sm.SnapshotState().Config, Group: s.group, InitialBalance: 100000}))
	s.store = db
	journal := newMemoryCompletionStore()
	s.SetInferenceCompletionStore(journal)
	s.mu.Lock()
	err = s.registerCompletionLocked(1, completionParams())
	s.mu.Unlock()
	require.NoError(t, err)
	require.NoError(t, db.Close())
	db, err = storage.NewSQLite(path)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	recovered, _, err := RecoverSession(db, creator, s.verifier, "escrow-1", testutil.RuntimeTestVersion, s.group, s.clients)
	require.NoError(t, err)
	recovered.SetInferenceCompletionStore(journal)
	return recovered, journal
}

func TestCompletionRestartClearsOrphanBeforeDrainOrProbe(t *testing.T) {
	for _, probe := range []bool{false, true} {
		t.Run(map[bool]string{false: "drain", true: "probe"}[probe], func(t *testing.T) {
			s, journal := restartWithCompletionOrphan(t)
			if probe {
				params := completionParams()
				params.Prompt = []byte(`{"messages":[{"role":"user","content":"probe"}]}`)
				p, err := s.PrepareInferenceFn(func(HostBinding) (InferenceParams, bool, error) { return params, true, nil })
				require.NoError(t, err)
				require.True(t, p.IsProbe())
			} else {
				require.NoError(t, s.SendPendingDiff(context.Background()))
			}
			require.Equal(t, uint64(1), s.Nonce())
			entries, err := journal.ListInferenceCompletions("escrow-1")
			require.NoError(t, err)
			require.Empty(t, entries)
			require.NoError(t, s.Finalize(context.Background()))
			require.Equal(t, types.PhaseSettlement, s.sm.Phase())
		})
	}
}

func TestCompletionOrphanDeletionFailurePreventsNonceReuse(t *testing.T) {
	for _, probe := range []bool{false, true} {
		t.Run(map[bool]string{false: "drain", true: "probe"}[probe], func(t *testing.T) {
			s, journal := restartWithCompletionOrphan(t)
			journal.deleteErr = errors.New("temporary journal failure")
			if probe {
				_, err := s.PrepareInferenceFn(func(HostBinding) (InferenceParams, bool, error) { return completionParams(), true, nil })
				require.ErrorIs(t, err, ErrInferenceCompletionPending)
			} else {
				require.ErrorIs(t, s.SendPendingDiff(context.Background()), ErrInferenceCompletionPending)
			}
			require.Zero(t, s.Nonce())
			diffs, err := s.store.GetDiffs("escrow-1", 1, 1)
			require.NoError(t, err)
			require.Empty(t, diffs)
			journal.deleteErr = nil
			require.NoError(t, s.SendPendingDiff(context.Background()))
			require.NoError(t, s.Finalize(context.Background()))
		})
	}
}

func TestCompletionFinalizeClearsPreviouslyConsumedOrphan(t *testing.T) {
	s, journal := restartWithCompletionOrphan(t)
	s.SetInferenceCompletionStore(nil)
	require.NoError(t, s.SendPendingDiff(context.Background()))
	s.SetInferenceCompletionStore(journal)
	require.NoError(t, s.Finalize(context.Background()))
	entries, err := journal.ListInferenceCompletions("escrow-1")
	require.NoError(t, err)
	require.Empty(t, entries)
}

func TestCompletionRestartHeartbeatClearsOrphan(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session")
	db, err := storage.NewSQLite(path)
	require.NoError(t, err)
	height, now := uint64(100), time.Unix(1000, 0)
	s, group, hosts, creator := setupRecoverableHeartbeatSession(t, db, &height, &now)
	journal := newMemoryCompletionStore()
	s.SetInferenceCompletionStore(journal)
	orphanNonce := s.Nonce() + 1
	s.mu.Lock()
	err = s.registerCompletionLocked(orphanNonce, completionParams())
	s.mu.Unlock()
	require.NoError(t, err)
	require.NoError(t, db.Close())
	db, err = storage.NewSQLite(path)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	recovered := recoverHeartbeatSession(t, db, group, hosts, creator, &height)
	recovered.SetInferenceCompletionStore(journal)
	require.NoError(t, recovered.MaybeHeartbeat(context.Background()))
	require.GreaterOrEqual(t, recovered.Nonce(), orphanNonce)
	diffs, err := db.GetDiffs("escrow-1", orphanNonce, orphanNonce)
	require.NoError(t, err)
	require.Len(t, diffs, 1)
	hasHeartbeat := false
	for _, tx := range diffs[0].Txs {
		require.Nil(t, tx.GetStartInference())
		hasHeartbeat = hasHeartbeat || tx.GetHeartbeat() != nil
	}
	require.True(t, hasHeartbeat)
	entries, err := journal.ListInferenceCompletions("escrow-1")
	require.NoError(t, err)
	require.Empty(t, entries)
	require.NoError(t, recovered.Finalize(context.Background()))
}

type completionHistoryFault struct {
	storage.Storage
	err     error
	missing bool
}

func (s *completionHistoryFault) GetDiffs(id string, from, to uint64) ([]types.DiffRecord, error) {
	if s.err != nil || s.missing {
		return nil, s.err
	}
	return s.Storage.GetDiffs(id, from, to)
}

func TestCompletionOrphanRequiresExactHistory(t *testing.T) {
	for _, unreadable := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing", true: "read_error"}[unreadable], func(t *testing.T) {
			s, journal := restartWithCompletionOrphan(t)
			s.SetInferenceCompletionStore(nil)
			require.NoError(t, s.SendPendingDiff(context.Background()))
			fault := &completionHistoryFault{Storage: s.store, missing: !unreadable}
			if unreadable {
				fault.err = errors.New("history unavailable")
			}
			s.store = fault
			s.SetInferenceCompletionStore(journal)
			require.ErrorIs(t, s.Finalize(context.Background()), ErrInferenceCompletionPending)
			entries, err := journal.ListInferenceCompletions("escrow-1")
			require.NoError(t, err)
			require.Len(t, entries, 1)
			require.Equal(t, types.PhaseActive, s.sm.Phase())
			fault.err, fault.missing = nil, false
			require.NoError(t, s.Finalize(context.Background()))
		})
	}
}

func TestCompletionHistoryReadFailurePreventsProbeReuse(t *testing.T) {
	s, journal := restartWithCompletionOrphan(t)
	s.store = &completionHistoryFault{Storage: s.store, err: errors.New("history unavailable")}
	_, err := s.PrepareInferenceFn(func(HostBinding) (InferenceParams, bool, error) { return completionParams(), true, nil })
	require.ErrorIs(t, err, ErrInferenceCompletionPending)
	require.Zero(t, s.Nonce())
	entries, err := journal.ListInferenceCompletions("escrow-1")
	require.NoError(t, err)
	require.Len(t, entries, 1)
}
