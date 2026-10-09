package storage

import (
	"bytes"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/types"
)

func testSnapshotImport(t *testing.T, s Storage) {
	p := defaultParams()
	require.NoError(t, s.CreateSession(p))
	data, err := types.RootedSnapshot([]byte("checkpoint"), bytes.Repeat([]byte{1}, 32))
	require.NoError(t, err)
	require.Error(t, s.ImportSnapshot(p.EscrowID, 10, []byte("no root")))
	require.NoError(t, s.ImportSnapshot(p.EscrowID, 10, data))
	meta, err := s.GetSessionMeta(p.EscrowID)
	require.NoError(t, err)
	require.Equal(t, uint64(10), meta.LatestNonce)
	require.Equal(t, uint64(10), meta.ImportedNonce)
	require.Zero(t, meta.LastFinalized)
	require.ErrorIs(t, s.AppendDiff(p.EscrowID, types.DiffRecord{Diff: types.Diff{Nonce: 1}}), ErrSnapshotAdvanced)
	require.ErrorIs(t, s.ImportSnapshot(p.EscrowID, 9, data), ErrSnapshotAdvanced)
	require.ErrorIs(t, s.ImportSnapshot(p.EscrowID, 10, data), ErrSnapshotAdvanced)
	require.NoError(t, s.SaveSnapshot(p.EscrowID, 10, []byte("stale writer")))
	n, got, err := s.LoadSnapshot(p.EscrowID)
	require.NoError(t, err)
	require.Equal(t, uint64(10), n)
	require.Equal(t, data, got)
	rows, err := s.GetDiffs(p.EscrowID, 1, 10)
	require.NoError(t, err)
	require.Empty(t, rows)
	require.NoError(t, s.AppendDiff(p.EscrowID, types.DiffRecord{Diff: types.Diff{Nonce: 11}}))
	meta, err = s.GetSessionMeta(p.EscrowID)
	require.NoError(t, err)
	require.Equal(t, uint64(11), meta.LatestNonce)
}

func TestSnapshotImportMemory(t *testing.T)   { testSnapshotImport(t, NewMemory()) }
func TestSnapshotImportSQLite(t *testing.T)   { testSnapshotImport(t, newTestSQLite(t)) }
func TestSnapshotImportPostgres(t *testing.T) { testSnapshotImport(t, newTestPostgres(t)) }

func testSnapshotImportRace(t *testing.T, s Storage) {
	p := defaultParams()
	require.NoError(t, s.CreateSession(p))
	data, err := types.RootedSnapshot([]byte("checkpoint"), bytes.Repeat([]byte{1}, 32))
	require.NoError(t, err)
	var a, b error
	var wg sync.WaitGroup
	wg.Add(2)
	start := make(chan struct{})
	go func() { defer wg.Done(); <-start; a = s.ImportSnapshot(p.EscrowID, 10, data) }()
	go func() {
		defer wg.Done()
		<-start
		b = s.AppendDiff(p.EscrowID, types.DiffRecord{Diff: types.Diff{Nonce: 11}})
	}()
	close(start)
	wg.Wait()
	require.NoError(t, b)
	if a != nil {
		require.ErrorIs(t, a, ErrSnapshotAdvanced)
	}
	meta, err := s.GetSessionMeta(p.EscrowID)
	require.NoError(t, err)
	require.Equal(t, uint64(11), meta.LatestNonce)
	if a == nil {
		require.Equal(t, uint64(10), meta.ImportedNonce)
	} else {
		require.Zero(t, meta.ImportedNonce)
	}
}
func TestSnapshotImportRaceSQLite(t *testing.T)   { testSnapshotImportRace(t, newTestSQLite(t)) }
func TestSnapshotImportRacePostgres(t *testing.T) { testSnapshotImportRace(t, newTestPostgres(t)) }

func TestSnapshotImportSQLiteReopen(t *testing.T) {
	dir := t.TempDir()
	s, err := NewSQLite(dir)
	require.NoError(t, err)
	p := defaultParams()
	require.NoError(t, s.CreateSession(p))
	data, err := types.RootedSnapshot([]byte("checkpoint"), bytes.Repeat([]byte{3}, 32))
	require.NoError(t, err)
	require.NoError(t, s.ImportSnapshot(p.EscrowID, 10, data))
	require.NoError(t, s.Close())
	s, err = NewSQLite(dir)
	require.NoError(t, err)
	defer s.Close()
	meta, err := s.GetSessionMeta(p.EscrowID)
	require.NoError(t, err)
	require.Equal(t, uint64(10), meta.ImportedNonce)
	n, got, err := s.LoadSnapshot(p.EscrowID)
	require.NoError(t, err)
	require.Equal(t, uint64(10), n)
	require.Equal(t, data, got)
}

func TestMigrateImportedSnapshot(t *testing.T) {
	src := newTestSQLite(t)
	dest := NewMemory()
	p := defaultParams()
	require.NoError(t, src.CreateSession(p))
	data, err := types.RootedSnapshot([]byte("checkpoint"), bytes.Repeat([]byte{3}, 32))
	require.NoError(t, err)
	require.NoError(t, src.ImportSnapshot(p.EscrowID, 10, data))
	require.NoError(t, src.AppendDiff(p.EscrowID, types.DiffRecord{Diff: types.Diff{Nonce: 11}}))
	require.NoError(t, migrateOneSQLiteSession(src, dest, p.EscrowID))
	require.NoError(t, migrateOneSQLiteSession(src, dest, p.EscrowID))
	meta, err := dest.GetSessionMeta(p.EscrowID)
	require.NoError(t, err)
	require.Equal(t, uint64(10), meta.ImportedNonce)
	require.Equal(t, uint64(11), meta.LatestNonce)
	require.ErrorIs(t, dest.AppendDiff(p.EscrowID, types.DiffRecord{Diff: types.Diff{Nonce: 2}}), ErrSnapshotAdvanced)
}

func TestSnapshotImportFailureSQLite(t *testing.T) {
	s := newTestSQLite(t)
	p := defaultParams()
	require.NoError(t, s.CreateSession(p))
	pool, _, err := s.poolFor(p.EscrowID)
	require.NoError(t, err)
	_, err = pool.writeDB.Exec(`CREATE TRIGGER fail_snapshot BEFORE INSERT ON snapshots BEGIN SELECT RAISE(ABORT, 'injected snapshot failure'); END`)
	require.NoError(t, err)
	data, err := types.RootedSnapshot([]byte("checkpoint"), bytes.Repeat([]byte{1}, 32))
	require.NoError(t, err)
	require.Error(t, s.ImportSnapshot(p.EscrowID, 10, data))
	meta, err := s.GetSessionMeta(p.EscrowID)
	require.NoError(t, err)
	require.Zero(t, meta.LatestNonce)
	require.Zero(t, meta.ImportedNonce)
	_, _, err = s.LoadSnapshot(p.EscrowID)
	require.ErrorIs(t, err, ErrSnapshotNotFound)
}
