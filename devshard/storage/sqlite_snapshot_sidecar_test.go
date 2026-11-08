package storage

import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSQLite_SnapshotWriteDoesNotBlockDiffJournal(t *testing.T) {
	db := newTestSQLite(t)
	params := defaultParams()
	require.NoError(t, db.CreateSession(params))
	p, _, err := db.poolFor(params.EscrowID)
	require.NoError(t, err)

	// An unfinished snapshot write holds the snapshot writer.
	tx, err := p.snapDB.Begin()
	require.NoError(t, err)
	_, err = tx.Exec(`INSERT INTO snapshots (escrow_id, nonce, state_data) VALUES (?, 1, x'00')`, params.EscrowID)
	require.NoError(t, err)
	defer tx.Rollback()

	done := make(chan error, 1)
	go func() { done <- db.AppendDiff(params.EscrowID, makeDiffRecord(1)) }()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("diff append waited for the snapshot writer")
	}
}

func TestSQLite_LoadSnapshotDoesNotWaitForSnapshotWriter(t *testing.T) {
	db := newTestSQLite(t)
	params := defaultParams()
	require.NoError(t, db.CreateSession(params))
	require.NoError(t, db.SaveSnapshot(params.EscrowID, 7, []byte("s")))
	p, _, err := db.poolFor(params.EscrowID)
	require.NoError(t, err)

	tx, err := p.snapDB.Begin()
	require.NoError(t, err)
	_, err = tx.Exec(`INSERT INTO snapshots (escrow_id, nonce, state_data) VALUES ('other', 1, x'00')`)
	require.NoError(t, err)
	defer tx.Rollback()

	done := make(chan error, 1)
	go func() { _, _, err := db.LoadSnapshot(params.EscrowID); done <- err }()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("snapshot load waited for the snapshot writer")
	}
}

func TestSQLite_LoadSnapshotReadsLegacyTable(t *testing.T) {
	db := newTestSQLite(t)
	params := defaultParams()
	require.NoError(t, db.CreateSession(params))
	p, _, err := db.poolFor(params.EscrowID)
	require.NoError(t, err)
	_, err = p.writeDB.Exec(`INSERT INTO snapshots (escrow_id, nonce, state_data) VALUES (?, 500, ?)`, params.EscrowID, []byte("legacy"))
	require.NoError(t, err)

	nonce, data, err := db.LoadSnapshot(params.EscrowID)
	require.NoError(t, err)
	require.Equal(t, uint64(500), nonce)
	require.Equal(t, []byte("legacy"), data)

	require.NoError(t, db.SaveSnapshot(params.EscrowID, 1000, []byte("sidecar")))
	nonce, data, err = db.LoadSnapshot(params.EscrowID)
	require.NoError(t, err)
	require.Equal(t, uint64(1000), nonce)
	require.Equal(t, []byte("sidecar"), data)
}

func TestSQLite_PruneEpochRemovesSnapshotFile(t *testing.T) {
	db := newTestSQLite(t)
	params := defaultParams()
	require.NoError(t, db.CreateSession(params))
	require.NoError(t, db.SaveSnapshot(params.EscrowID, 500, []byte("s")))
	path := db.snapshotFilePath(params.EpochID)
	_, err := os.Stat(path)
	require.NoError(t, err)

	require.NoError(t, db.PruneEpoch(params.EpochID))
	for _, f := range db.epochFiles(params.EpochID) {
		_, err := os.Stat(f)
		require.True(t, os.IsNotExist(err), f)
	}
}

func TestSQLite_PruneBeforeRemovesSnapshotFile(t *testing.T) {
	db := newTestSQLite(t)
	params := defaultParams()
	require.NoError(t, db.CreateSession(params))
	require.NoError(t, db.SaveSnapshot(params.EscrowID, 500, []byte("s")))
	// Only the sidecar is left, e.g. after an earlier partial removal.
	require.NoError(t, os.Remove(db.epochFilePath(params.EpochID)))

	require.NoError(t, db.pruneBefore(params.EpochID+1))
	for _, f := range db.epochFiles(params.EpochID) {
		_, err := os.Stat(f)
		require.True(t, os.IsNotExist(err), f)
	}
}

func TestQuarantineSQLiteArtifactsMovesSnapshotFile(t *testing.T) {
	dir := t.TempDir()
	src, err := NewSQLite(dir)
	require.NoError(t, err)
	params := paramsForEpoch("q", 1)
	require.NoError(t, src.CreateSession(params))
	require.NoError(t, src.SaveSnapshot(params.EscrowID, 500, []byte("s")))
	snapPath := src.snapshotFilePath(params.EpochID)
	require.NoError(t, src.Close())

	require.NoError(t, quarantineSQLiteArtifacts(dir))
	_, err = os.Stat(snapPath)
	require.True(t, os.IsNotExist(err))
}
