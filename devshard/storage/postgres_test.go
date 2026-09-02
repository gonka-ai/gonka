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

)

func postgresContainerWaitStrategy() wait.Strategy {
	return wait.ForAll(
		wait.ForLog("database system is ready to accept connections").
			WithOccurrence(2),
		wait.ForListeningPort("5432/tcp"),
	).WithStartupTimeout(60 * time.Second)
}

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
		testcontainers.WithWaitStrategy(postgresContainerWaitStrategy()),
	)
	require.NoError(t, err)

	host, err := container.Host(ctx)
	require.NoError(t, err)
	port, err := container.MappedPort(ctx, "5432/tcp")
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
	require.NoError(t, pg.WaitReady(context.Background()))
	return pg
}

// markPostgresIndexReadyForTest marks a manually constructed *Postgres as
// index-ready so escrow-keyed methods (CreateSession, SaveSnapshot, …) do not
// block on waitReadyForOp. NewPostgres already starts the async rebuild; this
// is only for tests that wire pool/maps directly.
func markPostgresIndexReadyForTest(pg *Postgres) {
	if pg.readyCh == nil {
		pg.readyCh = make(chan struct{})
	}
	pg.markIndexDone(nil)
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
func TestPostgres_CreateSession_EmptyVersionRejected(t *testing.T) {
	runCreateSession_EmptyVersionRejected(t, newTestPostgres(t))
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
func TestPostgres_SealedInferenceLifecycle(t *testing.T) {
	runSealedInferenceLifecycle(t, newTestPostgres(t))
}
func TestPostgres_ValidationObsBatchDrain(t *testing.T) {
	runValidationObsBatchDrain(t, newTestPostgres(t))
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
		"devshard_sealed_inferences_epoch_100", "devshard_sealed_inferences_epoch_101", "devshard_sealed_inferences_epoch_102",
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
		"devshard_sealed_inferences_epoch_100", "devshard_sealed_inferences_epoch_102",
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
		"devshard_sealed_inferences_epoch_105",
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

// TestPostgres_IndexRepair_MissingAndStale verifies indexExisting batch-repairs
// a diverged durable index: orphan index rows are deleted, missing rows are
// inserted, and already-matching rows are left alone.
func TestPostgres_IndexRepair_MissingAndStale(t *testing.T) {
	cleanup := setupPostgresContainer(t)
	defer cleanup()

	ctx := context.Background()
	pg, err := NewPostgres(ctx)
	require.NoError(t, err)

	require.NoError(t, pg.CreateSession(paramsForEpoch("keep", 10)))
	require.NoError(t, pg.CreateSession(paramsForEpoch("missing-idx", 11)))
	require.NoError(t, pg.ensurePartition(ctx, 12))
	_, err = pg.pool.Exec(ctx,
		`INSERT INTO devshard_sessions
		    (epoch_id, escrow_id, version, creator_addr, config_json, group_json, initial_balance)
		 VALUES (12, 'orphan-session', 'v-test', 'creator', '{}', '[]', 1)`)
	require.NoError(t, err)
	// Drop the index row that CreateSession wrote for missing-idx, leave keep intact,
	// and plant a stale index row with no matching session.
	_, err = pg.pool.Exec(ctx, `DELETE FROM devshard_session_index WHERE escrow_id = $1`, "missing-idx")
	require.NoError(t, err)
	_, err = pg.pool.Exec(ctx,
		`INSERT INTO devshard_session_index (escrow_id, epoch_id) VALUES ('stale-idx', 99)`)
	require.NoError(t, err)
	require.NoError(t, pg.Close())

	pg2, err := NewPostgres(ctx)
	require.NoError(t, err)
	defer pg2.Close()

	require.True(t, pg2.HasEscrow("keep"))
	require.True(t, pg2.HasEscrow("missing-idx"))
	require.True(t, pg2.HasEscrow("orphan-session"))
	require.False(t, pg2.HasEscrow("stale-idx"))

	var keepEpoch, missingEpoch, orphanEpoch uint64
	require.NoError(t, pg2.pool.QueryRow(ctx,
		`SELECT epoch_id FROM devshard_session_index WHERE escrow_id = $1`, "keep",
	).Scan(&keepEpoch))
	require.NoError(t, pg2.pool.QueryRow(ctx,
		`SELECT epoch_id FROM devshard_session_index WHERE escrow_id = $1`, "missing-idx",
	).Scan(&missingEpoch))
	require.NoError(t, pg2.pool.QueryRow(ctx,
		`SELECT epoch_id FROM devshard_session_index WHERE escrow_id = $1`, "orphan-session",
	).Scan(&orphanEpoch))
	require.Equal(t, uint64(10), keepEpoch)
	require.Equal(t, uint64(11), missingEpoch)
	require.Equal(t, uint64(12), orphanEpoch)

	var staleCount int
	require.NoError(t, pg2.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM devshard_session_index WHERE escrow_id = $1`, "stale-idx",
	).Scan(&staleCount))
	require.Equal(t, 0, staleCount)
}

func TestMissingSessionIndexRows_SkipsIndexed(t *testing.T) {
	sessions := map[string]uint64{"a": 1, "b": 2, "c": 3}
	indexedOK := map[string]struct{}{"a": {}, "c": {}}
	escrows, epochs := missingSessionIndexRows(sessions, indexedOK)
	require.Len(t, escrows, 1)
	require.Equal(t, "b", escrows[0])
	require.Equal(t, []int64{2}, epochs)
}

func TestForEachEscrowEpochBatch_ChunksBy1000(t *testing.T) {
	const n = postgresIndexRepairBatchSize + 3
	escrows := make([]string, n)
	epochs := make([]int64, n)
	for i := 0; i < n; i++ {
		escrows[i] = "e"
		epochs[i] = int64(i)
	}
	var sizes []int
	err := forEachEscrowEpochBatch(escrows, epochs, func(batchEscrows []string, batchEpochs []int64) error {
		require.Equal(t, len(batchEscrows), len(batchEpochs))
		sizes = append(sizes, len(batchEscrows))
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, []int{postgresIndexRepairBatchSize, 3}, sizes)
}

func TestPostgres_WaitReady_BlocksUntilIndex(t *testing.T) {
	cleanup := setupPostgresContainer(t)
	defer cleanup()

	release := make(chan struct{})
	entered := make(chan struct{})
	indexExistingHook = func(ctx context.Context) {
		close(entered)
		select {
		case <-release:
		case <-ctx.Done():
		}
	}
	t.Cleanup(func() { indexExistingHook = nil })

	ctx := context.Background()
	pg, err := NewPostgres(ctx)
	require.NoError(t, err)
	defer pg.Close()

	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("index hook did not start")
	}
	require.False(t, pg.Ready())

	waitErr := make(chan error, 1)
	go func() {
		waitCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		waitErr <- pg.WaitReady(waitCtx)
	}()

	select {
	case err := <-waitErr:
		t.Fatalf("WaitReady returned before index release: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	close(release)
	require.NoError(t, <-waitErr)
	require.True(t, pg.Ready())
	require.NoError(t, pg.CreateSession(paramsForEpoch("after-ready", 1)))
}

func TestPostgres_NewPostgres_ConnectBudgetDoesNotIncludeIndex(t *testing.T) {
	cleanup := setupPostgresContainer(t)
	defer cleanup()

	release := make(chan struct{})
	entered := make(chan struct{})
	indexExistingHook = func(ctx context.Context) {
		close(entered)
		select {
		case <-release:
		case <-ctx.Done():
		}
	}
	t.Cleanup(func() {
		indexExistingHook = nil
		select {
		case <-release:
		default:
			close(release)
		}
	})

	connectCtx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	pg, err := NewPostgres(connectCtx)
	require.NoError(t, err)
	defer pg.Close()

	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("index hook did not start")
	}
	require.False(t, pg.Ready(), "index must still be in progress after NewPostgres returns")
	canceled, cancelWait := context.WithCancel(context.Background())
	cancelWait()
	require.Error(t, pg.WaitReady(canceled))
}

func TestPostgres_LookupEpoch_FillsFromDurableIndex(t *testing.T) {
	cleanup := setupPostgresContainer(t)
	defer cleanup()

	ctx := context.Background()
	pg1, err := NewPostgres(ctx)
	require.NoError(t, err)
	require.NoError(t, pg1.WaitReady(ctx))
	require.NoError(t, pg1.CreateSession(paramsForEpoch("peer-created", 7)))
	require.NoError(t, pg1.Close())

	// Fresh handle: index rebuild loads the row. Clear the in-memory map to
	// simulate a peer that created the session after our rebuild finished.
	pg2, err := NewPostgres(ctx)
	require.NoError(t, err)
	defer pg2.Close()
	require.NoError(t, pg2.WaitReady(ctx))

	pg2.mu.Lock()
	delete(pg2.escrowIdx, "peer-created")
	pg2.mu.Unlock()
	require.False(t, func() bool {
		pg2.mu.RLock()
		defer pg2.mu.RUnlock()
		_, ok := pg2.escrowIdx["peer-created"]
		return ok
	}())

	meta, err := pg2.GetSessionMeta("peer-created")
	require.NoError(t, err)
	require.Equal(t, uint64(7), meta.EpochID)
	require.True(t, pg2.HasEscrow("peer-created"),
		"miss must backfill escrowIdx from durable session_index")
}

// TestPostgres_HAPeerCreateAfterBootIndex_StalesMemoryAndBackfills reproduces the
// multi-host race fixed for warm /mempool: instance A finishes async
// indexExisting, then peer B CreateSession's a new escrow. A's in-memory
// escrowIdx stays stale until lookupEpoch reads devshard_session_index.
func TestPostgres_HAPeerCreateAfterBootIndex_StalesMemoryAndBackfills(t *testing.T) {
	cleanup := setupPostgresContainer(t)
	defer cleanup()
	ctx := context.Background()

	// A is mid-lifecycle after boot: connect + migrate done, index rebuild done.
	starter, err := NewPostgres(ctx)
	require.NoError(t, err)
	defer starter.Close()
	require.NoError(t, starter.WaitReady(ctx))

	// B is a second process handle on the same Postgres (HA peer).
	peer, err := NewPostgres(ctx)
	require.NoError(t, err)
	defer peer.Close()
	require.NoError(t, peer.WaitReady(ctx))

	const (
		escrowID = "ha-peer-created-after-sibling-boot-index"
		epochID  = uint64(42)
	)

	// Peer accepts a new session while starter's boot-time index is already a
	// finished snapshot — starter.escrowIdx will not include this escrow.
	require.NoError(t, peer.CreateSession(paramsForEpoch(escrowID, epochID)))

	starter.mu.RLock()
	_, inMem := starter.escrowIdx[escrowID]
	starter.mu.RUnlock()
	require.False(t, inMem,
		"starter boot index must be outdated after peer CreateSession")

	// SessionServerExisting / warm paths call GetSessionMeta → lookupEpoch.
	// Without durable backfill this returned "session not found".
	meta, err := starter.GetSessionMeta(escrowID)
	require.NoError(t, err)
	require.Equal(t, epochID, meta.EpochID)
	require.True(t, starter.HasEscrow(escrowID))

	starter.mu.RLock()
	filled, ok := starter.escrowIdx[escrowID]
	starter.mu.RUnlock()
	require.True(t, ok, "lookup must backfill escrowIdx from durable index")
	require.Equal(t, epochID, filled)
}

// TestPostgres_HAPeerCreateWhileSiblingIndexRebuild_StillVisibleAfterReady covers
// the overlapping-start window: peer CreateSession while the sibling is still
// inside indexExisting (blocked in the test hook). When the rebuild runs after
// the create, the durable row is present; a later peer create after Ready still
// requires durable backfill on the starter.
func TestPostgres_HAPeerCreateWhileSiblingIndexRebuild_StillVisibleAfterReady(t *testing.T) {
	cleanup := setupPostgresContainer(t)
	defer cleanup()
	ctx := context.Background()

	peer, err := NewPostgres(ctx)
	require.NoError(t, err)
	defer peer.Close()
	require.NoError(t, peer.WaitReady(ctx))

	entered := make(chan struct{})
	release := make(chan struct{})
	indexExistingHook = func(context.Context) {
		close(entered)
		<-release
	}
	t.Cleanup(func() {
		indexExistingHook = nil
		select {
		case <-release:
		default:
			close(release)
		}
	})

	starterErr := make(chan error, 1)
	var starter *Postgres
	go func() {
		pg, err := NewPostgres(ctx)
		if err != nil {
			starterErr <- err
			return
		}
		starter = pg
		starterErr <- nil
	}()

	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("starter index rebuild did not start")
	}
	select {
	case err := <-starterErr:
		require.NoError(t, err, "NewPostgres must return before index finishes")
	case <-time.After(5 * time.Second):
		t.Fatal("NewPostgres did not return while index was blocked")
	}
	require.NotNil(t, starter)
	defer starter.Close()
	require.False(t, starter.Ready(), "starter still indexing")

	const (
		duringEscrow = "created-while-sibling-indexing"
		duringEpoch  = uint64(11)
		afterEscrow  = "created-after-sibling-ready"
		afterEpoch   = uint64(12)
	)
	require.NoError(t, peer.CreateSession(paramsForEpoch(duringEscrow, duringEpoch)))

	close(release)
	require.NoError(t, starter.WaitReady(ctx))

	// Create that landed before indexExisting's scan must be in starter memory.
	starter.mu.RLock()
	duringMem, duringOK := starter.escrowIdx[duringEscrow]
	starter.mu.RUnlock()
	require.True(t, duringOK, "indexExisting after peer create must load durable row")
	require.Equal(t, duringEpoch, duringMem)

	// Post-ready peer create is the stale-index race: memory miss + durable hit.
	require.NoError(t, peer.CreateSession(paramsForEpoch(afterEscrow, afterEpoch)))
	starter.mu.RLock()
	_, afterInMem := starter.escrowIdx[afterEscrow]
	starter.mu.RUnlock()
	require.False(t, afterInMem, "post-ready peer create must leave starter memory stale")

	meta, err := starter.GetSessionMeta(afterEscrow)
	require.NoError(t, err)
	require.Equal(t, afterEpoch, meta.EpochID)
	require.True(t, starter.HasEscrow(afterEscrow))
}

func TestMigrateLegacy_IntoPostgresStorage(t *testing.T) {
	cleanup := setupPostgresContainer(t)
	defer cleanup()

	legacyPath := writeLegacyDB(t, []legacyTestSession{
		{escrowID: "legacy-a", version: "", status: "active", balance: 1000, latestNonce: 2, lastFinalized: 1},
		{escrowID: "legacy-b", version: "", status: "active", balance: 2000, latestNonce: 1},
	})
	store, err := NewStorage(context.Background(), t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	pg, ok := store.(*HybridStorage).pg.(*Postgres)
	require.True(t, ok)

	n, err := MigrateLegacySQLite(legacyPath, store, func(escrowID string) (uint64, error) {
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
		WHERE p.relname IN ('devshard_sessions', 'devshard_diffs', 'devshard_signatures', 'devshard_snapshots', 'devshard_sealed_inferences')
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
