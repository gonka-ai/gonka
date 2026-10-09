package storage

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHasSQLiteSessions_missingMeta(t *testing.T) {
	dir := t.TempDir()
	ok, err := HasSQLiteSessions(dir)
	require.NoError(t, err)
	require.False(t, ok)
}

func TestHasSQLiteSessions_emptyEscrowEpoch(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(dir, 0o755))
	db, err := openMetaDB(MetaDBPath(dir))
	require.NoError(t, err)
	require.NoError(t, db.Close())

	ok, err := HasSQLiteSessions(dir)
	require.NoError(t, err)
	require.False(t, ok)
}

func TestHasSQLiteSessions_withRows(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, insertMetaEscrowRow(dir, "escrow-a", 7))

	ok, err := HasSQLiteSessions(dir)
	require.NoError(t, err)
	require.True(t, ok)
}

func TestHasSQLiteSessions_corruptMeta(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(MetaDBPath(dir), []byte("not-a-database"), 0o644))

	_, err := HasSQLiteSessions(dir)
	require.Error(t, err)
}

func TestReadPGBound_absent(t *testing.T) {
	dir := t.TempDir()
	ok, err := ReadPGBound(dir)
	require.NoError(t, err)
	require.False(t, ok)
}

func TestReadPGBound_present(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, WritePGBound(dir))
	ok, err := ReadPGBound(dir)
	require.NoError(t, err)
	require.True(t, ok)
}

func TestWritePGBound_atomic(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, WritePGBound(dir))
	ok, err := ReadPGBound(dir)
	require.NoError(t, err)
	require.True(t, ok)

	// Simulate a new process: path still readable.
	ok, err = ReadPGBound(dir)
	require.NoError(t, err)
	require.True(t, ok)
}

func insertMetaEscrowRow(storeDir, escrowID string, epochID uint64) error {
	if err := os.MkdirAll(storeDir, 0o755); err != nil {
		return err
	}
	db, err := openMetaDB(MetaDBPath(storeDir))
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.Exec(`INSERT INTO escrow_epoch (escrow_id, epoch_id) VALUES (?, ?)`, escrowID, epochID)
	return err
}

// Test flow:
//  1. Fill a store directory with epoch files, their sidecars, a malformed name and a directory named like an epoch file.
//  2. Require the newest real epoch file to win, and an empty directory to report none.
func TestNewestSQLiteEpoch_ignoresSidecarsAndDirectories(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"_meta.db", "epoch_7.db", "epoch_12.db", "epoch_40.db-wal", "epoch_x.db"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), nil, 0o600))
	}
	require.NoError(t, os.Mkdir(filepath.Join(dir, "epoch_90.db"), 0o755))

	newest, found, err := NewestSQLiteEpoch(dir)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, uint64(12), newest)

	_, found, err = NewestSQLiteEpoch(t.TempDir())
	require.NoError(t, err)
	require.False(t, found)
}
