package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"sort"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"devshard/types"
)

// setupPostgresContainer spins a fresh PG container per test and points the
// pgx env vars at it. Mirrors the pattern from
// decentralized-api/payloadstorage/postgres_storage_test.go so dapi-side
// regressions and devshard-side regressions are caught the same way.
func setupPostgresContainer(t *testing.T) func() {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping postgres testcontainers tests in -short mode (requires Docker)")
	}

	ctx := context.Background()
	container, err := postgres.Run(ctx,
		"postgres:18.1-bookworm",
		postgres.WithDatabase("testdb"),
		postgres.WithUsername("testuser"),
		postgres.WithPassword("testpass"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(60*time.Second),
		),
	)
	require.NoError(t, err)

	host, err := container.Host(ctx)
	require.NoError(t, err)
	port, err := container.MappedPort(ctx, "5432")
	require.NoError(t, err)

	t.Setenv("PGHOST", host)
	t.Setenv("PGPORT", port.Port())
	t.Setenv("PGDATABASE", "testdb")
	t.Setenv("PGUSER", "testuser")
	t.Setenv("PGPASSWORD", "testpass")

	return func() { _ = container.Terminate(ctx) }
}

func newTestPostgres(t *testing.T) *Postgres {
	t.Helper()
	cleanup := setupPostgresContainer(t)
	t.Cleanup(cleanup)

	pg, err := NewPostgres(context.Background())
	require.NoError(t, err)
	t.Cleanup(func() { _ = pg.Close() })
	return pg
}

func captureStorageLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	currentLogger := slog.Default()
	t.Cleanup(func() { slog.SetDefault(currentLogger) })
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	return &buf
}

func readStorageLogEntries(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(buf.Bytes()))
	var entries []map[string]any
	for {
		var entry map[string]any
		err := decoder.Decode(&entry)
		if errors.Is(err, io.EOF) {
			return entries
		}
		require.NoError(t, err)
		entries = append(entries, entry)
	}
}

func requireStorageLogEntry(t *testing.T, entries []map[string]any, msg string) map[string]any {
	t.Helper()
	for _, entry := range entries {
		if entry["msg"] == msg {
			return entry
		}
	}
	require.Failf(t, "missing log entry", "msg=%q entries=%v", msg, entries)
	return nil
}

// Conformance suite -- every test that the Memory and SQLite backends pass
// must also pass against real Postgres. Catches schema drift between backends.

func TestPostgres_CreateSession_GetSessionMeta(t *testing.T) {
	runCreateSession_GetSessionMeta(t, newTestPostgres(t))
}
func TestPostgres_CreateSession_Idempotent(t *testing.T) {
	runCreateSession_Idempotent(t, newTestPostgres(t))
}
func TestPostgres_CreateSession_ConflictingEpoch(t *testing.T) {
	runCreateSession_ConflictingEpoch(t, newTestPostgres(t))
}
func TestPostgres_CreateSession_ConflictingVersion(t *testing.T) {
	runCreateSession_ConflictingVersion(t, newTestPostgres(t))
}
func TestPostgres_AppendDiff_GetDiffs(t *testing.T) {
	runAppendDiff_GetDiffs(t, newTestPostgres(t))
}
func TestPostgres_GetSignatures(t *testing.T) {
	runGetSignatures(t, newTestPostgres(t))
}
func TestPostgres_MarkFinalized_LastFinalized(t *testing.T) {
	runMarkFinalized_LastFinalized(t, newTestPostgres(t))
}
func TestPostgres_SaveLoadSnapshot(t *testing.T) {
	runSaveLoadSnapshot(t, newTestPostgres(t))
}
func TestPostgres_AddSignature(t *testing.T) {
	runAddSignature(t, newTestPostgres(t))
}
func TestPostgres_WarmKeyDelta(t *testing.T) {
	runWarmKeyDelta(t, newTestPostgres(t))
}
func TestPostgres_MarkSettled(t *testing.T) {
	runMarkSettled(t, newTestPostgres(t))
}
func TestPostgres_ListActiveSessions(t *testing.T) {
	runListActiveSessions(t, newTestPostgres(t))
}
func TestPostgres_PruneEpoch_RemovesOnlyTarget(t *testing.T) {
	runPruneEpoch_RemovesOnlyTarget(t, newTestPostgres(t))
}
func TestPostgres_PruneEpoch_Idempotent(t *testing.T) {
	runPruneEpoch_Idempotent(t, newTestPostgres(t))
}
func TestPostgres_PruneEpoch_WriteAfter(t *testing.T) {
	runPruneEpoch_WriteAfter(t, newTestPostgres(t))
}

// TestPostgres_PartitionTablesPhysicallyDropped is the assertion specific to
// the Postgres backend: PruneEpoch must DROP the per-epoch partition tables,
// not just delete rows from them. We query pg_class directly so a regression
// to "DELETE FROM ... WHERE epoch_id = ..." would fail this test.
func TestPostgres_PartitionTablesPhysicallyDropped(t *testing.T) {
	pg := newTestPostgres(t)

	// Create sessions in three epochs so we have three sets of partitions.
	require.NoError(t, pg.CreateSession(paramsForEpoch("a", 100)))
	require.NoError(t, pg.CreateSession(paramsForEpoch("b", 101)))
	require.NoError(t, pg.CreateSession(paramsForEpoch("c", 102)))

	for _, esc := range []string{"a", "b", "c"} {
		require.NoError(t, pg.AppendDiff(esc, makeDiffRecord(1)))
		require.NoError(t, pg.AddSignature(esc, 1, 1, []byte("sig")))
	}
	require.Equal(t, 1, countSessionIndexRows(t, pg.pool, 101))

	// All per-epoch partition tables should exist.
	require.Equal(t, []string{
		"devshard_diffs_epoch_100", "devshard_diffs_epoch_101", "devshard_diffs_epoch_102",
		"devshard_sessions_epoch_100", "devshard_sessions_epoch_101", "devshard_sessions_epoch_102",
		"devshard_signatures_epoch_100", "devshard_signatures_epoch_101", "devshard_signatures_epoch_102",
		"devshard_snapshots_epoch_100", "devshard_snapshots_epoch_101", "devshard_snapshots_epoch_102",
	}, listDevshardPartitions(t, pg.pool))

	// Drop the middle epoch.
	require.NoError(t, pg.PruneEpoch(101))
	require.Equal(t, 0, countSessionIndexRows(t, pg.pool, 101))

	// Only epoch 101's partitions are gone; the others survive.
	require.Equal(t, []string{
		"devshard_diffs_epoch_100", "devshard_diffs_epoch_102",
		"devshard_sessions_epoch_100", "devshard_sessions_epoch_102",
		"devshard_signatures_epoch_100", "devshard_signatures_epoch_102",
		"devshard_snapshots_epoch_100", "devshard_snapshots_epoch_102",
	}, listDevshardPartitions(t, pg.pool))

	// And the surviving epochs still have their data accessible.
	for _, esc := range []string{"a", "c"} {
		meta, err := pg.GetSessionMeta(esc)
		require.NoError(t, err, "session %s should survive prune", esc)
		require.Equal(t, uint64(1), meta.LatestNonce)
	}

	// Pruning a non-existent epoch is a no-op.
	require.NoError(t, pg.PruneEpoch(999))
}

func TestPostgres_PruneBefore_DropsOnlyExistingOldPartitions(t *testing.T) {
	pg := newTestPostgres(t)

	require.NoError(t, pg.CreateSession(paramsForEpoch("a", 100)))
	require.NoError(t, pg.CreateSession(paramsForEpoch("b", 101)))
	require.NoError(t, pg.CreateSession(paramsForEpoch("c", 105)))
	for _, esc := range []string{"a", "b", "c"} {
		require.NoError(t, pg.AppendDiff(esc, makeDiffRecord(1)))
	}

	require.NoError(t, pg.pruneBefore(102))

	require.Equal(t, []string{
		"devshard_diffs_epoch_105",
		"devshard_sessions_epoch_105",
		"devshard_signatures_epoch_105",
		"devshard_snapshots_epoch_105",
	}, listDevshardPartitions(t, pg.pool))
	require.Equal(t, 0, countSessionIndexRows(t, pg.pool, 100))
	require.Equal(t, 0, countSessionIndexRows(t, pg.pool, 101))
	require.Equal(t, 1, countSessionIndexRows(t, pg.pool, 105))

	_, err := pg.GetSessionMeta("a")
	require.ErrorIs(t, err, ErrSessionNotFound)
	meta, err := pg.GetSessionMeta("c")
	require.NoError(t, err)
	require.Equal(t, uint64(1), meta.LatestNonce)
}

func countSessionIndexRows(t *testing.T, pool *pgxpool.Pool, epochID uint64) int {
	t.Helper()
	var count int
	err := pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM devshard_session_index WHERE epoch_id = $1`,
		epochID,
	).Scan(&count)
	require.NoError(t, err)
	return count
}

// TestPostgres_RecoversIndexAcrossReopen verifies that a fresh Postgres
// handle rebuilds its escrow_id -> epoch_id index by scanning
// devshard_sessions on startup, so subsequent reads route correctly without
// re-creating the session.
func TestPostgres_RecoversIndexAcrossReopen(t *testing.T) {
	cleanup := setupPostgresContainer(t)
	defer cleanup()

	ctx := context.Background()

	pg1, err := NewPostgres(ctx)
	require.NoError(t, err)

	require.NoError(t, pg1.CreateSession(paramsForEpoch("e", 42)))
	require.NoError(t, pg1.AppendDiff("e", makeDiffRecord(1)))
	require.NoError(t, pg1.AppendDiff("e", makeDiffRecord(2)))
	require.NoError(t, pg1.MarkFinalized("e", 2))
	require.NoError(t, pg1.Close())

	// Reopen with a fresh handle. Without index rebuild, GetSessionMeta would
	// return ErrSessionNotFound because lookupEpoch can't route the read.
	pg2, err := NewPostgres(ctx)
	require.NoError(t, err)
	defer pg2.Close()

	meta, err := pg2.GetSessionMeta("e")
	require.NoError(t, err)
	require.Equal(t, uint64(42), meta.EpochID)
	require.Equal(t, uint64(2), meta.LatestNonce)
	require.Equal(t, uint64(2), meta.LastFinalized)

	diffs, err := pg2.GetDiffs("e", 1, 2)
	require.NoError(t, err)
	require.Len(t, diffs, 2)
}

func TestHybrid_StickySQLiteThenPostgresReconnect(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping hybrid postgres test in -short mode (requires Docker)")
	}

	t.Setenv("PGHOST", "127.0.0.1")
	t.Setenv("PGPORT", "1")
	t.Setenv("PGDATABASE", "missing")
	t.Setenv("PGUSER", "missing")
	t.Setenv("PGPASSWORD", "missing")

	sqlite, err := NewSQLite(t.TempDir())
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	hybrid := NewHybridStorage(ctx, sqlite, time.Millisecond, defaultPGConnectTimeout)
	cancel()
	defer func() {
		if hybrid != nil {
			_ = hybrid.Close()
		}
	}()

	require.Nil(t, hybrid.currentPostgres())
	require.NoError(t, hybrid.CreateSession(paramsForEpoch("sqlite", 1)))
	require.NoError(t, hybrid.AppendDiff("sqlite", makeDiffRecord(1)))

	cleanup := setupPostgresContainer(t)
	defer cleanup()

	require.NoError(t, hybrid.CreateSession(paramsForEpoch("pg", 2)))
	pg := hybrid.currentPostgres()
	require.NotNil(t, pg)

	_, err = pg.GetSessionMeta("pg")
	require.NoError(t, err)
	_, err = pg.GetSessionMeta("sqlite")
	require.ErrorIs(t, err, ErrSessionNotFound)

	require.NoError(t, hybrid.AppendDiff("sqlite", makeDiffRecord(2)))
	sqliteMeta, err := sqlite.GetSessionMeta("sqlite")
	require.NoError(t, err)
	require.Equal(t, uint64(2), sqliteMeta.LatestNonce)

	require.NoError(t, hybrid.Close())
	hybrid = nil

	sqliteAfterRestart, err := NewSQLite(sqlite.baseDir)
	require.NoError(t, err)
	hybridAfterRestart := NewHybridStorage(context.Background(), sqliteAfterRestart, time.Millisecond, defaultPGConnectTimeout)
	t.Cleanup(func() { _ = hybridAfterRestart.Close() })

	require.NotNil(t, hybridAfterRestart.currentPostgres())
	require.NoError(t, hybridAfterRestart.AppendDiff("sqlite", makeDiffRecord(3)))
	restartedMeta, err := sqliteAfterRestart.GetSessionMeta("sqlite")
	require.NoError(t, err)
	require.Equal(t, uint64(3), restartedMeta.LatestNonce)
}

func TestHybrid_ListActiveSessionsSkipsDuplicateEscrow(t *testing.T) {
	cleanup := setupPostgresContainer(t)
	defer cleanup()

	sqlite, err := NewSQLite(t.TempDir())
	require.NoError(t, err)
	hybrid := NewHybridStorage(context.Background(), sqlite, 240*time.Second, defaultPGConnectTimeout)
	t.Cleanup(func() { _ = hybrid.Close() })
	pg := hybrid.currentPostgres()
	require.NotNil(t, pg)

	require.NoError(t, sqlite.CreateSession(paramsForEpoch("dup", 7)))
	require.NoError(t, pg.CreateSession(paramsForEpoch("dup", 7)))

	logs := captureStorageLogs(t)
	active, err := hybrid.ListActiveSessions()
	require.NoError(t, err)
	require.Equal(t, []ActiveSession{{EscrowID: "dup", EpochID: 7}}, active)

	route, ok := hybrid.remembered("dup")
	require.True(t, ok)
	require.Equal(t, hybridSQLite, route.backend)

	duplicateLog := requireStorageLogEntry(t, readStorageLogEntries(t, logs),
		"devshard storage: duplicate active session in sqlite and postgres, using sqlite copy")
	require.Equal(t, types.LegacySessionVersion, duplicateLog["sqlite_version"])
	require.Equal(t, types.LegacySessionVersion, duplicateLog["postgres_version"])
}

func TestHybrid_PruneEpochPrunesBothBackendsAndRoutes(t *testing.T) {
	cleanup := setupPostgresContainer(t)
	defer cleanup()

	sqlite, err := NewSQLite(t.TempDir())
	require.NoError(t, err)
	hybrid := NewHybridStorage(context.Background(), sqlite, 240*time.Second, defaultPGConnectTimeout)
	t.Cleanup(func() { _ = hybrid.Close() })
	pg := hybrid.currentPostgres()
	require.NotNil(t, pg)

	require.NoError(t, sqlite.CreateSession(paramsForEpoch("sqlite", 4)))
	require.NoError(t, sqlite.AppendDiff("sqlite", makeDiffRecord(1)))
	_, err = hybrid.GetSessionMeta("sqlite")
	require.NoError(t, err)

	require.NoError(t, hybrid.CreateSession(paramsForEpoch("pg", 4)))
	require.NoError(t, hybrid.AppendDiff("pg", makeDiffRecord(1)))

	hybrid.mu.Lock()
	require.Len(t, hybrid.routes, 2)
	hybrid.mu.Unlock()

	require.NoError(t, hybrid.PruneEpoch(4))

	_, err = sqlite.GetSessionMeta("sqlite")
	require.ErrorIs(t, err, ErrSessionNotFound)
	_, err = pg.GetSessionMeta("pg")
	require.ErrorIs(t, err, ErrSessionNotFound)

	hybrid.mu.Lock()
	require.Empty(t, hybrid.routes)
	hybrid.mu.Unlock()
}

func TestHybrid_PruneBeforePrunesBothBackendsAndRoutes(t *testing.T) {
	cleanup := setupPostgresContainer(t)
	defer cleanup()

	sqlite, err := NewSQLite(t.TempDir())
	require.NoError(t, err)
	hybrid := NewHybridStorage(context.Background(), sqlite, 240*time.Second, defaultPGConnectTimeout)
	t.Cleanup(func() { _ = hybrid.Close() })
	pg := hybrid.currentPostgres()
	require.NotNil(t, pg)

	require.NoError(t, sqlite.CreateSession(paramsForEpoch("sqlite-old", 2)))
	require.NoError(t, sqlite.CreateSession(paramsForEpoch("sqlite-new", 8)))
	for _, esc := range []string{"sqlite-old", "sqlite-new"} {
		require.NoError(t, sqlite.AppendDiff(esc, makeDiffRecord(1)))
		_, err = hybrid.GetSessionMeta(esc)
		require.NoError(t, err)
	}

	require.NoError(t, hybrid.CreateSession(paramsForEpoch("pg-old", 2)))
	require.NoError(t, hybrid.CreateSession(paramsForEpoch("pg-new", 8)))
	require.NoError(t, hybrid.pruneBefore(5))

	_, err = sqlite.GetSessionMeta("sqlite-old")
	require.ErrorIs(t, err, ErrSessionNotFound)
	_, err = pg.GetSessionMeta("pg-old")
	require.ErrorIs(t, err, ErrSessionNotFound)
	_, err = sqlite.GetSessionMeta("sqlite-new")
	require.NoError(t, err)
	_, err = pg.GetSessionMeta("pg-new")
	require.NoError(t, err)

	hybrid.mu.Lock()
	require.NotContains(t, hybrid.routes, "sqlite-old")
	require.NotContains(t, hybrid.routes, "pg-old")
	require.Contains(t, hybrid.routes, "sqlite-new")
	require.Contains(t, hybrid.routes, "pg-new")
	hybrid.mu.Unlock()
}

func TestHybrid_PruneBeforeReturnsErrorWhenPostgresUnavailable(t *testing.T) {
	t.Setenv("PGHOST", "127.0.0.1")
	t.Setenv("PGPORT", "1")
	t.Setenv("PGDATABASE", "missing")
	t.Setenv("PGUSER", "missing")
	t.Setenv("PGPASSWORD", "missing")

	sqlite, err := NewSQLite(t.TempDir())
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	hybrid := NewHybridStorage(ctx, sqlite, time.Hour, defaultPGConnectTimeout)
	cancel()
	t.Cleanup(func() { _ = hybrid.Close() })
	require.Nil(t, hybrid.currentPostgres())

	require.NoError(t, hybrid.CreateSession(paramsForEpoch("sqlite-old", 2)))
	require.NoError(t, hybrid.CreateSession(paramsForEpoch("sqlite-new", 8)))

	err = hybrid.pruneBefore(5)
	require.ErrorContains(t, err, "postgres backend unavailable for prune")

	_, err = sqlite.GetSessionMeta("sqlite-old")
	require.ErrorIs(t, err, ErrSessionNotFound)
	_, err = sqlite.GetSessionMeta("sqlite-new")
	require.NoError(t, err)
}

func TestHybrid_PostgresRoutedSessionDoesNotFallbackWhenPostgresUnavailable(t *testing.T) {
	cleanup := setupPostgresContainer(t)
	defer cleanup()

	sqlite, err := NewSQLite(t.TempDir())
	require.NoError(t, err)
	hybrid := NewHybridStorage(context.Background(), sqlite, 240*time.Second, defaultPGConnectTimeout)
	t.Cleanup(func() { _ = hybrid.Close() })
	pg := hybrid.currentPostgres()
	require.NotNil(t, pg)

	require.NoError(t, hybrid.CreateSession(paramsForEpoch("pg-only", 9)))
	require.NoError(t, hybrid.AppendDiff("pg-only", makeDiffRecord(1)))

	hybrid.mu.Lock()
	hybrid.pg = nil
	hybrid.mu.Unlock()
	pg.Close()

	err = hybrid.AppendDiff("pg-only", makeDiffRecord(2))
	require.ErrorContains(t, err, "postgres backend unavailable")
	_, err = sqlite.GetSessionMeta("pg-only")
	require.ErrorIs(t, err, ErrSessionNotFound)
}

func TestMigrateLegacy_IntoHybridUsesPostgresWhenAvailable(t *testing.T) {
	cleanup := setupPostgresContainer(t)
	defer cleanup()

	legacyPath := writeLegacyDB(t, []legacyTestSession{
		{escrowID: "legacy-a", version: "", status: "active", balance: 1000, latestNonce: 2, lastFinalized: 1},
		{escrowID: "legacy-b", version: "", status: "active", balance: 2000, latestNonce: 1},
	})
	sqlite, err := NewSQLite(t.TempDir())
	require.NoError(t, err)
	hybrid := NewHybridStorage(context.Background(), sqlite, 240*time.Second, defaultPGConnectTimeout)
	t.Cleanup(func() { _ = hybrid.Close() })
	pg := hybrid.currentPostgres()
	require.NotNil(t, pg)

	n, err := MigrateLegacySQLite(legacyPath, hybrid, func(escrowID string) (uint64, error) {
		switch escrowID {
		case "legacy-a":
			return 20, nil
		case "legacy-b":
			return 21, nil
		default:
			return 0, ErrSkipLegacySession
		}
	})
	require.NoError(t, err)
	require.Equal(t, 2, n)

	for _, escrowID := range []string{"legacy-a", "legacy-b"} {
		_, err = pg.GetSessionMeta(escrowID)
		require.NoError(t, err)
		_, err = sqlite.GetSessionMeta(escrowID)
		require.ErrorIs(t, err, ErrSessionNotFound)
	}
}

// listDevshardPartitions returns every devshard_*_epoch_<N> partition that
// currently exists, sorted, so the assertion is order-stable.
func listDevshardPartitions(t *testing.T, pool *pgxpool.Pool) []string {
	t.Helper()
	rows, err := pool.Query(context.Background(), `
		SELECT c.relname
		FROM pg_class c
		JOIN pg_inherits i ON i.inhrelid = c.oid
		JOIN pg_class p ON p.oid = i.inhparent
		WHERE p.relname IN ('devshard_sessions', 'devshard_diffs', 'devshard_signatures', 'devshard_snapshots')
		ORDER BY c.relname
	`)
	require.NoError(t, err)
	defer rows.Close()

	var names []string
	for rows.Next() {
		var name string
		require.NoError(t, rows.Scan(&name))
		names = append(names, name)
	}
	require.NoError(t, rows.Err())
	sort.Strings(names)
	if names == nil {
		return []string{}
	}
	return names
}
