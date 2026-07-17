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

func TestHasSQLiteArtifacts_missingStore(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "missing")
	ok, err := HasSQLiteArtifacts(dir)
	require.NoError(t, err)
	require.False(t, ok)
}

func TestHasSQLiteArtifacts_metaDB(t *testing.T) {
	dir := t.TempDir()
	db, err := openMetaDB(MetaDBPath(dir))
	require.NoError(t, err)
	require.NoError(t, db.Close())

	ok, err := HasSQLiteArtifacts(dir)
	require.NoError(t, err)
	require.True(t, ok)
}

func TestHasSQLiteArtifacts_epochDB(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "epoch_7.db"), []byte{}, 0o644))

	ok, err := HasSQLiteArtifacts(dir)
	require.NoError(t, err)
	require.True(t, ok)
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
