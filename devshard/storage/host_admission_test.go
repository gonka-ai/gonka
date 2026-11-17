package storage

import (
	"context"
	"testing"

	"devshard/types"
	"github.com/stretchr/testify/require"
)

func checkHostAdmissionPrefix(t *testing.T, store Storage) {
	t.Helper()
	require.NoError(t, store.CreateSession(defaultParams()))
	one := makeDiffRecord(1)
	require.NoError(t, store.AppendDiff("escrow-1", one))
	meta, err := store.GetSessionMeta("escrow-1")
	require.NoError(t, err)
	require.Zero(t, meta.HostAdmissionNonce)
	two := makeDiffRecord(2)
	two.HostAdmitted = true
	require.ErrorIs(t, PersistDiffBatch(context.Background(), store, "escrow-1", []types.DiffRecord{two}), ErrHostAdmissionGap)
	records, err := store.GetDiffs("escrow-1", 2, 2)
	require.NoError(t, err)
	require.Empty(t, records, "failed promotion must roll back the diff too")
	bad := one
	bad.StateHash = []byte("fork")
	bad.HostAdmitted = true
	require.ErrorIs(t, store.AppendDiff("escrow-1", bad), ErrDiffFork)
	meta, err = store.GetSessionMeta("escrow-1")
	require.NoError(t, err)
	require.Zero(t, meta.HostAdmissionNonce)
	one.HostAdmitted = true
	require.NoError(t, store.AppendDiff("escrow-1", one))
	three := makeDiffRecord(3)
	three.HostAdmitted = true
	require.NoError(t, PersistDiffBatch(context.Background(), store, "escrow-1", []types.DiffRecord{two, three}))
	// An HA retry neither regresses the mark nor exports write authority.
	require.NoError(t, store.AppendDiff("escrow-1", one))
	meta, err = store.GetSessionMeta("escrow-1")
	require.NoError(t, err)
	require.Equal(t, uint64(3), meta.HostAdmissionNonce)
	records, err = store.GetDiffs("escrow-1", 1, 3)
	require.NoError(t, err)
	require.Len(t, records, 3)
	for _, rec := range records {
		require.False(t, rec.HostAdmitted, "read records carry no host admission write intent")
	}
	four := makeDiffRecord(4)
	require.NoError(t, store.AppendDiff("escrow-1", four))
	five := makeDiffRecord(5)
	five.HostAdmitted = true
	require.ErrorIs(t, store.AppendDiff("escrow-1", five), ErrHostAdmissionGap)
	six := makeDiffRecord(6)
	six.HostAdmitted = true
	four.HostAdmitted = true
	require.ErrorIs(t, PersistDiffBatch(context.Background(), store, "escrow-1", []types.DiffRecord{four, six}), ErrHostAdmissionGap)
	meta, err = store.GetSessionMeta("escrow-1")
	require.NoError(t, err)
	require.Equal(t, uint64(3), meta.HostAdmissionNonce)
	require.Equal(t, uint64(4), meta.LatestNonce)
}

func TestHostAdmissionPrefixMemory(t *testing.T)   { checkHostAdmissionPrefix(t, NewMemory()) }
func TestHostAdmissionPrefixSQLite(t *testing.T)   { checkHostAdmissionPrefix(t, newTestSQLite(t)) }
func TestHostAdmissionPrefixPostgres(t *testing.T) { checkHostAdmissionPrefix(t, newTestPostgres(t)) }

func TestHostAdmissionPrefixSQLiteSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	store, err := NewSQLite(dir)
	require.NoError(t, err)
	checkHostAdmissionPrefix(t, store)
	require.NoError(t, store.Close())
	store, err = NewSQLite(dir)
	require.NoError(t, err)
	defer store.Close()
	meta, err := store.GetSessionMeta("escrow-1")
	require.NoError(t, err)
	require.Equal(t, uint64(3), meta.HostAdmissionNonce)
	require.Equal(t, uint64(4), meta.LatestNonce)
}

func TestHostAdmissionSQLiteMigrationPreservesOnlyAcceptedPrefix(t *testing.T) {
	src := newTestSQLite(t)
	checkHostAdmissionPrefix(t, src) // accepted 1..3, unmarked 4
	dest := NewMemory()
	for range 2 {
		_, err := MigrateSQLiteSessions(src, dest)
		require.NoError(t, err)
		meta, err := dest.GetSessionMeta("escrow-1")
		require.NoError(t, err)
		require.Equal(t, uint64(3), meta.HostAdmissionNonce)
		require.Equal(t, uint64(4), meta.LatestNonce)
	}
	five := makeDiffRecord(5)
	five.HostAdmitted = true
	require.ErrorIs(t, dest.AppendDiff("escrow-1", five), ErrHostAdmissionGap)
}
