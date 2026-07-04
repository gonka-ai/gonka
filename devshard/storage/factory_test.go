package storage

import (
	"context"
	"os"
	"sort"
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
	_, ok = hybrid.pg.(*Postgres)
	require.True(t, ok, "expected postgres backend")
	require.Nil(t, hybrid.sqlite, "sqlite must not be attached without legacy sessions")

	_, err = os.Stat(MetaDBPath(storeDir))
	require.True(t, os.IsNotExist(err), "sqlite meta must not be opened in postgres mode")

	pgBound, err := ReadPGBound(storeDir)
	require.NoError(t, err)
	require.False(t, pgBound, "empty postgres has no sessions to orphan, so .pg-bound must not be set")
}

func TestNewStorage_pgBoundLifecycleTracksPGSessions(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping postgres factory test in -short mode (requires Docker)")
	}
	cleanup := setupPostgresContainer(t)
	defer cleanup()

	storeDir := t.TempDir()
	store, err := NewStorage(context.Background(), storeDir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	pgBound, err := ReadPGBound(storeDir)
	require.NoError(t, err)
	require.False(t, pgBound, "empty postgres must not set .pg-bound at boot")

	params := paramsForEpoch("pg-escrow", 10)
	params.Version = storageTestVersion
	require.NoError(t, store.CreateSession(params))

	pgBound, err = ReadPGBound(storeDir)
	require.NoError(t, err)
	require.True(t, pgBound, "creating a postgres session must write .pg-bound")

	require.NoError(t, store.PruneEpoch(10))

	pgBound, err = ReadPGBound(storeDir)
	require.NoError(t, err)
	require.False(t, pgBound, "draining the last postgres session must clear .pg-bound")
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

func TestNewStorage_attachesSQLiteAndPostgresWhenMetaHasRowsAndPGHOSTSet(t *testing.T) {
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
	_, ok := hybrid.sqlite.(*SQLite)
	require.True(t, ok, "legacy sqlite escrows must still be served")
	_, ok = hybrid.pg.(*Postgres)
	require.True(t, ok, "postgres must back new escrows")
	require.True(t, hybrid.preferPG, "new escrows must prefer postgres")

	requireStorageLogEntry(t, readStorageLogEntries(t, logs),
		"devshard storage: serving legacy sqlite escrows alongside postgres; they drain in place as they settle and prune while new escrows go to postgres")
}

// TestNewStorage_sqliteEscrowSurvivesPostgresEnablement exercises the full
// transition: a store starts SQLite-only (no PGHOST) with a live escrow, then
// PGHOST is enabled on the next boot. The pre-existing escrow must keep running
// on SQLite while brand-new escrows are created in Postgres.
func TestNewStorage_sqliteEscrowSurvivesPostgresEnablement(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping postgres factory test in -short mode (requires Docker)")
	}
	cleanup := setupPostgresContainer(t)
	defer cleanup()
	pgHost := os.Getenv("PGHOST")

	storeDir := t.TempDir()
	ctx := context.Background()

	// Phase 1: SQLite only. Create an escrow and write to it.
	t.Setenv("PGHOST", "")
	sqliteStore, err := NewStorage(ctx, storeDir)
	require.NoError(t, err)
	h1 := sqliteStore.(*HybridStorage)
	require.Nil(t, h1.pg, "phase 1 must be sqlite-only")
	_, ok := h1.sqlite.(*SQLite)
	require.True(t, ok)

	require.NoError(t, sqliteStore.CreateSession(paramsForEpoch("sqlite-escrow", 5)))
	require.NoError(t, sqliteStore.AppendDiff("sqlite-escrow", makeDiffRecord(1)))
	require.NoError(t, sqliteStore.Close())

	// Phase 2: enable Postgres. Both backends attach.
	t.Setenv("PGHOST", pgHost)
	store, err := NewStorage(ctx, storeDir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	h := store.(*HybridStorage)
	sqlite, ok := h.sqlite.(*SQLite)
	require.True(t, ok, "legacy sqlite backend must stay attached")
	pg, ok := h.pg.(*Postgres)
	require.True(t, ok, "postgres backend must attach")

	// The pre-existing escrow is still served and stays physically in SQLite.
	meta, err := store.GetSessionMeta("sqlite-escrow")
	require.NoError(t, err)
	require.Equal(t, uint64(5), meta.EpochID)
	require.Equal(t, uint64(1), meta.LatestNonce)
	require.True(t, sqlite.HasEscrow("sqlite-escrow"))
	require.False(t, pg.HasEscrow("sqlite-escrow"))

	// It keeps accepting writes on SQLite after Postgres is enabled.
	require.NoError(t, store.AppendDiff("sqlite-escrow", makeDiffRecord(2)))
	diffs, err := store.GetDiffs("sqlite-escrow", 1, 2)
	require.NoError(t, err)
	require.Len(t, diffs, 2)
	require.True(t, sqlite.HasEscrow("sqlite-escrow"))
	require.False(t, pg.HasEscrow("sqlite-escrow"))

	// A new escrow is created in Postgres, not SQLite.
	require.NoError(t, store.CreateSession(paramsForEpoch("pg-escrow", 6)))
	require.True(t, pg.HasEscrow("pg-escrow"), "new escrow must be created in postgres")
	require.False(t, sqlite.HasEscrow("pg-escrow"), "new escrow must not touch sqlite")

	require.NoError(t, store.AppendDiff("pg-escrow", makeDiffRecord(1)))
	pgDiffs, err := store.GetDiffs("pg-escrow", 1, 1)
	require.NoError(t, err)
	require.Len(t, pgDiffs, 1)

	// Recovery surfaces both escrows together.
	active, err := store.ListActiveSessions()
	require.NoError(t, err)
	ids := make([]string, 0, len(active))
	for _, a := range active {
		ids = append(ids, a.EscrowID)
	}
	sort.Strings(ids)
	require.Equal(t, []string{"pg-escrow", "sqlite-escrow"}, ids)

	// A live Postgres session now exists, so .pg-bound is set.
	pgBound, err := ReadPGBound(storeDir)
	require.NoError(t, err)
	require.True(t, pgBound)
}

func TestNewStorage_sqliteWhenMetaHasRowsPGHOSTUnset(t *testing.T) {
	t.Setenv("PGHOST", "")
	storeDir := t.TempDir()
	require.NoError(t, insertMetaEscrowRow(storeDir, "local", 1))

	store, err := NewStorage(context.Background(), storeDir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	hybrid := store.(*HybridStorage)
	_, ok := hybrid.sqlite.(*SQLite)
	require.True(t, ok)
	require.Nil(t, hybrid.pg, "postgres must not be attached without PGHOST")
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

	_, ok := store.(*HybridStorage).pg.(*Postgres)
	require.True(t, ok)

	pgBound, err := ReadPGBound(storeDir)
	require.NoError(t, err)
	require.False(t, pgBound, "empty postgres has no sessions to orphan, so .pg-bound must not be set")
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

	_, ok := store.(*HybridStorage).sqlite.(*SQLite)
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
