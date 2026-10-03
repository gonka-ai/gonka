package storage

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func runPendingFinishes(t *testing.T, s Storage) {
	t.Helper()
	ps, ok := s.(PendingFinishStore)
	require.True(t, ok)
	require.NoError(t, s.CreateSession(defaultParams()))

	got, err := ps.PendingFinishes("escrow-1")
	require.NoError(t, err)
	require.Empty(t, got)

	require.NoError(t, ps.PutPendingFinish("escrow-1", 3, []byte{1}))
	require.NoError(t, ps.PutPendingFinish("escrow-1", 3, []byte{2}))
	require.NoError(t, ps.PutPendingFinish("escrow-1", 5, []byte{5}))
	got, err = ps.PendingFinishes("escrow-1")
	require.NoError(t, err)
	require.Equal(t, map[uint64][]byte{3: {2}, 5: {5}}, got)

	require.NoError(t, s.PruneEpoch(defaultParams().EpochID))
	got, _ = ps.PendingFinishes("escrow-1")
	require.Empty(t, got)
}

func TestMemory_PendingFinishes(t *testing.T) {
	runPendingFinishes(t, NewMemory())
}

func TestSQLite_PendingFinishes(t *testing.T) {
	runPendingFinishes(t, newTestSQLite(t))
}

func TestSQLite_PendingFinishesSurviveReopen(t *testing.T) {
	dir := t.TempDir()
	db1, err := NewSQLite(dir)
	require.NoError(t, err)
	require.NoError(t, db1.CreateSession(defaultParams()))
	require.NoError(t, db1.PutPendingFinish("escrow-1", 9, []byte("finish")))
	require.NoError(t, db1.Close())

	db2, err := NewSQLite(dir)
	require.NoError(t, err)
	defer db2.Close()
	got, err := db2.PendingFinishes("escrow-1")
	require.NoError(t, err)
	require.Equal(t, map[uint64][]byte{9: []byte("finish")}, got)
}

func TestPostgres_PendingFinishes(t *testing.T) {
	runPendingFinishes(t, newTestPostgres(t))
}
