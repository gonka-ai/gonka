package storage

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

)

func TestNewStorage_postgresWhenPGHOSTAndEmptyMeta(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping postgres factory test in -short mode (requires Docker)")
	}
	cleanup := setupPostgresContainer(t)
	defer cleanup()

	storeDir := t.TempDir()
	ctx := context.Background()

	store, err := NewStorage(ctx, storeDir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	hybrid, ok := store.(*HybridStorage)
	require.True(t, ok)
	_, ok = hybrid.backend.(*Postgres)
	require.True(t, ok, "expected postgres backend")

	_, err = os.Stat(MetaDBPath(storeDir))
	require.True(t, os.IsNotExist(err), "sqlite meta must not be opened in postgres mode")

	pgBound, err := ReadPGBound(storeDir)
	require.NoError(t, err)
	require.True(t, pgBound)
}

func TestNewStorage_postgresBootFailsWhenUnreachable(t *testing.T) {
	t.Setenv("PGHOST", "127.0.0.1")
	t.Setenv("PGPORT", "1")
	t.Setenv("PGDATABASE", "missing")
	t.Setenv("PGUSER", "missing")
	t.Setenv("PGPASSWORD", "missing")

	storeDir := t.TempDir()
	_, err := NewStorage(context.Background(), storeDir)
	require.Error(t, err)
	require.Contains(t, err.Error(), "postgres storage")

	_, err = os.Stat(MetaDBPath(storeDir))
	require.True(t, os.IsNotExist(err))
}

func TestNewStorage_sqliteWhenMetaHasRowsAndPGHOSTSet(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping postgres factory test in -short mode (requires Docker)")
	}
	cleanup := setupPostgresContainer(t)
	defer cleanup()

	storeDir := t.TempDir()
	require.NoError(t, insertMetaEscrowRow(storeDir, "drain-me", 3))

	logs := captureStorageLogs(t)
	store, err := NewStorage(context.Background(), storeDir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	hybrid := store.(*HybridStorage)
	_, ok := hybrid.backend.(*SQLite)
	require.True(t, ok)

	requireStorageLogEntry(t, readStorageLogEntries(t, logs),
		"devshard storage: draining sqlite sessions while PGHOST is set; settle and prune until escrow_epoch is empty, then restart for postgres-only mode")
}

func TestNewStorage_sqliteWhenMetaHasRowsPGHOSTUnset(t *testing.T) {
	t.Setenv("PGHOST", "")
	storeDir := t.TempDir()
	require.NoError(t, insertMetaEscrowRow(storeDir, "local", 1))

	store, err := NewStorage(context.Background(), storeDir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	hybrid := store.(*HybridStorage)
	_, ok := hybrid.backend.(*SQLite)
	require.True(t, ok)
}

func TestNewStorage_postgresWhenEmptyMetaAndPGHOST(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping postgres factory test in -short mode (requires Docker)")
	}
	cleanup := setupPostgresContainer(t)
	defer cleanup()

	storeDir := t.TempDir()
	db, err := openMetaDB(MetaDBPath(storeDir))
	require.NoError(t, err)
	require.NoError(t, db.Close())

	store, err := NewStorage(context.Background(), storeDir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	_, ok := store.(*HybridStorage).backend.(*Postgres)
	require.True(t, ok)

	pgBound, err := ReadPGBound(storeDir)
	require.NoError(t, err)
	require.True(t, pgBound)
}

func TestNewStorage_failsWhenPGBoundWithoutPGHOST(t *testing.T) {
	t.Setenv("PGHOST", "")
	storeDir := t.TempDir()
	require.NoError(t, WritePGBound(storeDir))

	_, err := NewStorage(context.Background(), storeDir)
	require.ErrorIs(t, err, ErrStoragePGBoundWithoutPostgres)
}

func TestNewStorage_PGBoundWithEmptyMetaDB(t *testing.T) {
	t.Setenv("PGHOST", "")
	storeDir := t.TempDir()
	require.NoError(t, WritePGBound(storeDir))

	db, err := openMetaDB(MetaDBPath(storeDir))
	require.NoError(t, err)
	require.NoError(t, db.Close())

	_, err = NewStorage(context.Background(), storeDir)
	require.ErrorIs(t, err, ErrStoragePGBoundWithoutPostgres)
}

func TestNewStorage_freshSQLiteWithoutPGHOST(t *testing.T) {
	t.Setenv("PGHOST", "")
	storeDir := t.TempDir()

	store, err := NewStorage(context.Background(), storeDir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	_, ok := store.(*HybridStorage).backend.(*SQLite)
	require.True(t, ok)
}

func TestNewStorage_postgresModeNoForkWhenPGDownAfterSessionInPG(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping postgres factory test in -short mode (requires Docker)")
	}

	cleanup := setupPostgresContainer(t)
	defer cleanup()

	storeDir := t.TempDir()
	store, err := NewStorage(context.Background(), storeDir)
	require.NoError(t, err)

	params := paramsForEpoch("pg-escrow", 10)
	params.Version = storageTestVersion
	require.NoError(t, store.CreateSession(params))
	require.NoError(t, store.Close())

	t.Setenv("PGHOST", "127.0.0.1")
	t.Setenv("PGPORT", "1")
	t.Setenv("PGDATABASE", "missing")
	t.Setenv("PGUSER", "missing")
	t.Setenv("PGPASSWORD", "missing")

	_, err = NewStorage(context.Background(), storeDir)
	require.Error(t, err, "postgres mode must fail boot when PG is down")

	_, err = os.Stat(MetaDBPath(storeDir))
	require.True(t, os.IsNotExist(err), "must not open sqlite when postgres mode boot fails")
}

