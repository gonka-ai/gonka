package storage

import (
	"context"
	"fmt"
	"os"
	"sort"
	"sync"
	"testing"
	"time"

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

func TestNewStorage_postgresBootDegradesWhenUnreachable(t *testing.T) {
	t.Setenv("PGHOST", "127.0.0.1")
	t.Setenv("PGPORT", "1")
	t.Setenv("PGDATABASE", "missing")
	t.Setenv("PGUSER", "missing")
	t.Setenv("PGPASSWORD", "missing")

	storeDir := t.TempDir()
	store, err := NewStorage(context.Background(), storeDir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	hybrid := store.(*HybridStorage)
	require.True(t, hybrid.degradedOwnerOnly)
	require.Nil(t, hybrid.sqlite)
	require.Nil(t, hybrid.pg)
	err = store.CreateSession(paramsForEpoch("new-escrow", 10))
	require.ErrorIs(t, err, ErrStoragePostgresUnavailable)

	_, err = os.Stat(MetaDBPath(storeDir))
	require.True(t, os.IsNotExist(err))
}

func TestNewStorage_pgUnavailableServesSQLiteOwnedOnlyAndRejectsNew(t *testing.T) {
	storeDir := t.TempDir()

	t.Setenv("PGHOST", "")
	sqliteStore, err := NewStorage(context.Background(), storeDir)
	require.NoError(t, err)
	require.NoError(t, sqliteStore.CreateSession(paramsForEpoch("sqlite-owned", 9)))
	require.NoError(t, sqliteStore.AppendDiff("sqlite-owned", makeDiffRecord(1)))
	require.NoError(t, sqliteStore.Close())

	t.Setenv("PGHOST", "127.0.0.1")
	t.Setenv("PGPORT", "1")
	t.Setenv("PGDATABASE", "missing")
	t.Setenv("PGUSER", "missing")
	t.Setenv("PGPASSWORD", "missing")

	logs := captureStorageLogs(t)
	store, err := NewStorage(context.Background(), storeDir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	requireStorageLogEntry(t, readStorageLogEntries(t, logs),
		"devshard storage: postgres unavailable; entering degraded mode while reconnect runs")

	hybrid := store.(*HybridStorage)
	require.True(t, hybrid.degradedOwnerOnly)
	require.Nil(t, hybrid.pg)
	sqlite := hybrid.sqlite.(*SQLite)
	require.True(t, sqlite.HasEscrow("sqlite-owned"))

	meta, err := store.GetSessionMeta("sqlite-owned")
	require.NoError(t, err)
	require.Equal(t, uint64(1), meta.LatestNonce)
	require.NoError(t, store.AppendDiff("sqlite-owned", makeDiffRecord(2)))

	err = store.CreateSession(paramsForEpoch("new-escrow", 10))
	require.ErrorIs(t, err, ErrStoragePostgresUnavailable)
	require.False(t, sqlite.HasEscrow("new-escrow"), "new escrow must not fall back to SQLite")
}

func TestNewStorage_pgUnavailableReconnectsAndPromotes(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping postgres factory test in -short mode (requires Docker)")
	}
	storeDir := t.TempDir()
	t.Setenv("PG_RECONNECT_INTERVAL", "20ms")

	t.Setenv("PGHOST", "")
	sqliteStore, err := NewStorage(context.Background(), storeDir)
	require.NoError(t, err)
	require.NoError(t, sqliteStore.CreateSession(paramsForEpoch("sqlite-owned", 9)))
	require.NoError(t, sqliteStore.Close())

	t.Setenv("PGHOST", "127.0.0.1")
	t.Setenv("PGPORT", "1")
	t.Setenv("PGDATABASE", "missing")
	t.Setenv("PGUSER", "missing")
	t.Setenv("PGPASSWORD", "missing")

	store, err := NewStorage(context.Background(), storeDir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	hybrid := store.(*HybridStorage)
	require.True(t, hybrid.degradedOwnerOnly)
	require.Nil(t, hybrid.pg)

	cleanup := setupPostgresContainer(t)
	t.Cleanup(cleanup)

	require.Eventually(t, func() bool {
		hybrid.mu.RLock()
		pg := hybrid.pg
		degraded := hybrid.degradedOwnerOnly
		hybrid.mu.RUnlock()
		return pg != nil && !degraded
	}, 30*time.Second, 50*time.Millisecond)

	sqlite := hybrid.sqlite.(*SQLite)
	pg := hybrid.pg.(*Postgres)
	require.True(t, sqlite.HasEscrow("sqlite-owned"))
	require.False(t, pg.HasEscrow("sqlite-owned"))
	require.NoError(t, store.CreateSession(paramsForEpoch("pg-after-reconnect", 10)))
	require.True(t, pg.HasEscrow("pg-after-reconnect"))
	require.False(t, sqlite.HasEscrow("pg-after-reconnect"), "new escrow must not fall back to SQLite after promotion")
}

func TestNewStorage_pgUnavailableEmptyStoreReconnectsAndPromotes(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping postgres factory test in -short mode (requires Docker)")
	}
	storeDir := t.TempDir()
	t.Setenv("PG_RECONNECT_INTERVAL", "20ms")

	t.Setenv("PGHOST", "127.0.0.1")
	t.Setenv("PGPORT", "1")
	t.Setenv("PGDATABASE", "missing")
	t.Setenv("PGUSER", "missing")
	t.Setenv("PGPASSWORD", "missing")

	store, err := NewStorage(context.Background(), storeDir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	hybrid := store.(*HybridStorage)
	require.True(t, hybrid.degradedOwnerOnly)
	require.Nil(t, hybrid.sqlite)
	require.Nil(t, hybrid.pg)

	err = store.CreateSession(paramsForEpoch("new-before-pg", 10))
	require.ErrorIs(t, err, ErrStoragePostgresUnavailable)
	_, err = os.Stat(MetaDBPath(storeDir))
	require.True(t, os.IsNotExist(err), "empty degraded boot must not create sqlite meta")

	cleanup := setupPostgresContainer(t)
	t.Cleanup(cleanup)

	require.Eventually(t, func() bool {
		hybrid.mu.RLock()
		pg := hybrid.pg
		degraded := hybrid.degradedOwnerOnly
		hybrid.mu.RUnlock()
		return pg != nil && !degraded
	}, 30*time.Second, 50*time.Millisecond)

	require.NoError(t, store.CreateSession(paramsForEpoch("pg-after-empty-reconnect", 10)))
	require.True(t, hybrid.pg.(*Postgres).HasEscrow("pg-after-empty-reconnect"))
}

func TestNewStorage_attachesSQLiteAndPostgresWhenMetaHasRowsAndPGHOSTSet(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping postgres factory test in -short mode (requires Docker)")
	}
	cleanup := setupPostgresContainer(t)
	defer cleanup()

	storeDir := t.TempDir()
	sqliteSeed, err := NewSQLite(storeDir)
	require.NoError(t, err)
	require.NoError(t, sqliteSeed.CreateSession(paramsForEpoch("drain-me", 3)))
	require.NoError(t, sqliteSeed.Close())

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

func TestNewStorage_pgHostSetReconcilesSQLiteEpochFilesWhenMetaRowsMissing(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping postgres factory test in -short mode (requires Docker)")
	}
	cleanup := setupPostgresContainer(t)
	defer cleanup()
	pgHost := os.Getenv("PGHOST")

	storeDir := t.TempDir()
	ctx := context.Background()

	t.Setenv("PGHOST", "")
	sqliteStore, err := NewStorage(ctx, storeDir)
	require.NoError(t, err)
	require.NoError(t, sqliteStore.CreateSession(paramsForEpoch("recovered-sqlite", 5)))
	require.NoError(t, sqliteStore.AppendDiff("recovered-sqlite", makeDiffRecord(1)))
	require.NoError(t, sqliteStore.Close())

	metaDB, err := openMetaDB(MetaDBPath(storeDir))
	require.NoError(t, err)
	_, err = metaDB.Exec(`DELETE FROM escrow_epoch`)
	require.NoError(t, err)
	require.NoError(t, metaDB.Close())

	hasRows, err := HasSQLiteSessions(storeDir)
	require.NoError(t, err)
	require.False(t, hasRows, "the old factory probe would miss the recoverable SQLite session")

	t.Setenv("PGHOST", pgHost)
	store, err := NewStorage(ctx, storeDir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	h := store.(*HybridStorage)
	sqlite, ok := h.sqlite.(*SQLite)
	require.True(t, ok, "SQLite must attach after reconciliation repairs _meta.db")
	pg, ok := h.pg.(*Postgres)
	require.True(t, ok)

	meta, err := store.GetSessionMeta("recovered-sqlite")
	require.NoError(t, err)
	require.Equal(t, uint64(5), meta.EpochID)
	require.Equal(t, uint64(1), meta.LatestNonce)
	require.True(t, sqlite.HasEscrow("recovered-sqlite"))
	require.False(t, pg.HasEscrow("recovered-sqlite"))

	require.NoError(t, store.CreateSession(paramsForEpoch("new-pg", 6)))
	require.True(t, pg.HasEscrow("new-pg"))
	require.False(t, sqlite.HasEscrow("new-pg"))
}

func TestNewStorage_sqliteToPostgresManySessionsMixedStatusConcurrent(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping postgres factory test in -short mode (requires Docker)")
	}
	cleanup := setupPostgresContainer(t)
	defer cleanup()
	pgHost := os.Getenv("PGHOST")

	const legacyCount = 60
	const newCount = 40

	storeDir := t.TempDir()
	ctx := context.Background()

	t.Setenv("PGHOST", "")
	sqliteStore, err := NewStorage(ctx, storeDir)
	require.NoError(t, err)
	for i := 0; i < legacyCount; i++ {
		escrowID := fmt.Sprintf("legacy-%03d", i)
		require.NoError(t, sqliteStore.CreateSession(paramsForEpoch(escrowID, 20+uint64(i%4))))
		if i%3 == 0 {
			require.NoError(t, sqliteStore.MarkSettled(escrowID))
			continue
		}
		require.NoError(t, sqliteStore.AppendDiff(escrowID, makeDiffRecord(1)))
		require.NoError(t, sqliteStore.AppendDiff(escrowID, makeDiffRecord(2)))
	}
	require.NoError(t, sqliteStore.Close())

	t.Setenv("PGHOST", pgHost)
	store, err := NewStorage(ctx, storeDir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	h := store.(*HybridStorage)
	sqlite, ok := h.sqlite.(*SQLite)
	require.True(t, ok, "legacy SQLite sessions must attach for drain")
	pg, ok := h.pg.(*Postgres)
	require.True(t, ok)

	var wg sync.WaitGroup
	errs := make(chan error, legacyCount+newCount)
	for i := 0; i < legacyCount; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			escrowID := fmt.Sprintf("legacy-%03d", i)
			meta, err := store.GetSessionMeta(escrowID)
			if err != nil {
				errs <- err
				return
			}
			if i%3 == 0 {
				if meta.Status != "settled" {
					errs <- fmt.Errorf("%s status = %s, want settled", escrowID, meta.Status)
				}
				return
			}
			if meta.Status != "active" {
				errs <- fmt.Errorf("%s status = %s, want active", escrowID, meta.Status)
				return
			}
			errs <- store.AppendDiff(escrowID, makeDiffRecord(3))
		}()
	}
	for i := 0; i < newCount; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			escrowID := fmt.Sprintf("pg-%03d", i)
			if err := store.CreateSession(paramsForEpoch(escrowID, 30+uint64(i%4))); err != nil {
				errs <- err
				return
			}
			if err := store.AppendDiff(escrowID, makeDiffRecord(1)); err != nil {
				errs <- err
				return
			}
			if i%4 == 0 {
				errs <- store.MarkSettled(escrowID)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}

	for i := 0; i < legacyCount; i++ {
		escrowID := fmt.Sprintf("legacy-%03d", i)
		require.True(t, sqlite.HasEscrow(escrowID), "%s must remain SQLite-owned", escrowID)
		require.False(t, pg.HasEscrow(escrowID), "%s must not be recreated in PG", escrowID)
	}
	for i := 0; i < newCount; i++ {
		escrowID := fmt.Sprintf("pg-%03d", i)
		require.True(t, pg.HasEscrow(escrowID), "%s must be PG-owned", escrowID)
		require.False(t, sqlite.HasEscrow(escrowID), "%s must not be created in SQLite", escrowID)
	}

	active, err := store.ListActiveSessions()
	require.NoError(t, err)
	gotActive := make([]string, 0, len(active))
	for _, session := range active {
		gotActive = append(gotActive, session.EscrowID)
	}
	sort.Strings(gotActive)

	var wantActive []string
	for i := 0; i < legacyCount; i++ {
		if i%3 != 0 {
			wantActive = append(wantActive, fmt.Sprintf("legacy-%03d", i))
		}
	}
	for i := 0; i < newCount; i++ {
		if i%4 != 0 {
			wantActive = append(wantActive, fmt.Sprintf("pg-%03d", i))
		}
	}
	sort.Strings(wantActive)
	require.Equal(t, wantActive, gotActive)

	for _, escrowID := range []string{"legacy-000", "pg-000"} {
		meta, err := store.GetSessionMeta(escrowID)
		require.NoError(t, err)
		require.Equal(t, "settled", meta.Status)
	}
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

func TestNewStorage_pgBoundWithoutPGHOSTServesSQLiteOwnedOnlyAndRejectsNew(t *testing.T) {
	t.Setenv("PGHOST", "")
	storeDir := t.TempDir()
	sqliteStore, err := NewStorage(context.Background(), storeDir)
	require.NoError(t, err)
	require.NoError(t, sqliteStore.CreateSession(paramsForEpoch("sqlite-owned", 9)))
	require.NoError(t, sqliteStore.Close())
	require.NoError(t, WritePGBound(storeDir))

	logs := captureStorageLogs(t)
	store, err := NewStorage(context.Background(), storeDir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	requireStorageLogEntry(t, readStorageLogEntries(t, logs),
		"devshard storage: .pg-bound present but PGHOST unset; serving sqlite-owned escrows only and rejecting new escrows")

	hybrid := store.(*HybridStorage)
	require.True(t, hybrid.degradedOwnerOnly)
	require.Nil(t, hybrid.pg)
	sqlite := hybrid.sqlite.(*SQLite)
	require.True(t, sqlite.HasEscrow("sqlite-owned"))

	_, err = store.GetSessionMeta("sqlite-owned")
	require.NoError(t, err)
	err = store.CreateSession(paramsForEpoch("new-escrow", 10))
	require.ErrorIs(t, err, ErrStoragePGBoundWithoutPostgres)
	require.False(t, sqlite.HasEscrow("new-escrow"), "new escrow must not fall back to SQLite")
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

	degraded, err := NewStorage(context.Background(), storeDir)
	require.NoError(t, err, "postgres mode should degrade instead of failing boot when PG is down")
	t.Cleanup(func() { _ = degraded.Close() })

	err = degraded.CreateSession(paramsForEpoch("new-while-pg-down", 10))
	require.ErrorIs(t, err, ErrStoragePostgresUnavailable)

	_, err = os.Stat(MetaDBPath(storeDir))
	require.True(t, os.IsNotExist(err), "must not open sqlite when postgres mode degrades without sqlite artifacts")
}
