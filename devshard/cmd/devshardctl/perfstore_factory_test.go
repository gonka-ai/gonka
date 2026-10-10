package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"common/storage/mode"

	"github.com/stretchr/testify/require"
)

// Test flow:
//  1. Select sqlite mode with PGHOST pointing at nothing.
//  2. The local perf.db is opened, and it is the store that also prunes request accounting.
func TestNewPerfStoreModeSQLiteIgnoresPGHOST(t *testing.T) {
	t.Setenv(mode.EnvStorageMode, "sqlite")
	t.Setenv("PGHOST", "127.0.0.1")
	t.Setenv("PGPORT", "1")

	store, err := NewPerfStore(context.Background(), t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	_, isSQLite := store.(*sqlitePerfStore)
	require.True(t, isSQLite)
	_, prunesAccounting := store.(accountingPruneStore)
	require.True(t, prunesAccounting)
}

// Test flow:
//  1. Select hybrid or postgres mode without PGHOST, then with PGHOST on a closed port.
//  2. Both opens fail instead of falling back to the local file.
func TestNewPerfStoreFailsClosedWhenPostgresIsRequired(t *testing.T) {
	for _, storageMode := range []string{"hybrid", "postgres"} {
		t.Run(storageMode+"_without_pghost", func(t *testing.T) {
			t.Setenv(mode.EnvStorageMode, storageMode)
			t.Setenv("PGHOST", "")
			_, err := NewPerfStore(context.Background(), t.TempDir())
			require.ErrorContains(t, err, "requires PGHOST")
		})
		t.Run(storageMode+"_postgres_down", func(t *testing.T) {
			t.Setenv(mode.EnvStorageMode, storageMode)
			t.Setenv("PGHOST", "127.0.0.1")
			t.Setenv("PGPORT", "1")
			t.Setenv("PG_CONNECT_TIMEOUT", "100ms")
			_, err := NewPerfStore(context.Background(), t.TempDir())
			require.ErrorContains(t, err, "postgres required")
		})
	}
}

// Test flow:
//  1. Fill a local perf.db: a live sample past the walk's end, a backfilled and a recent sample, two requests more than the log reads, and a request with an attempt and a cached alias.
//  2. Open the store in postgres mode: startup loads what it loaded from the file, the sample past the walk is left behind, accounting resolves, the backfill does not repeat, and Postgres never prunes the shared accounting.
//  3. Write after the import, then reopen: the import does not run again and new ids do not collide.
func TestNewPerfStoreImportsWhatStartupReadsOnceThenServesPostgres(t *testing.T) {
	ctx := context.Background()
	storageDir := t.TempDir()
	localPath := filepath.Join(storageDir, "perf.db")
	legacyPath := filepath.Join(t.TempDir(), "escrow-12-state.db")
	legacy, err := newSQLitePerfStore(legacyPath)
	require.NoError(t, err)
	now := time.Now().UTC().Truncate(time.Millisecond)
	legacySample := perfContractSample("", now.Add(-2*time.Minute))
	legacySample.HostIdx = 0
	require.NoError(t, legacy.InsertSample(ctx, legacySample))
	require.NoError(t, legacy.Close())

	local, err := newSQLitePerfStore(localPath)
	require.NoError(t, err)
	require.NoError(t, local.InsertSample(ctx, perfContractSample("participant-old", now.Add(-5*time.Hour))))
	backfilled, err := local.BackfillLegacyEscrowSamples(ctx, "escrow-12", legacyPath, []string{"participant-legacy"})
	require.NoError(t, err)
	require.Len(t, backfilled, 1)
	require.NoError(t, local.InsertSample(ctx, perfContractSample("participant-recent", now.Add(-time.Minute))))
	for index := range requestLogSize + 2 {
		require.NoError(t, local.InsertRequest(ctx, perfContractRequest(index)))
	}
	startedAt := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	require.NoError(t, local.UpsertAccountingRequest(ctx, "request-1", "escrow-1", "Qwen/Test", startedAt))
	require.NoError(t, local.UpsertAccountingAttempt(ctx, RequestAccountingAttempt{RequestID: "request-1", EscrowID: "escrow-1", Nonce: 3, CreatedAt: startedAt}))
	require.NoError(t, local.UpsertAccountingAlias(ctx, "request-2", "escrow-2", "request-1", "escrow-1", "cache_hit", startedAt))
	localSamples, err := local.LoadSamples(ctx)
	require.NoError(t, err)
	localRequests, err := local.LoadRequests(ctx)
	require.NoError(t, err)
	require.NoError(t, local.Close())

	t.Cleanup(setupPostgresContainer(t))
	t.Setenv(mode.EnvStorageMode, "postgres")
	store, err := NewPerfStore(ctx, storageDir)
	require.NoError(t, err)
	postgres, isPostgres := store.(*postgresPerfStore)
	require.True(t, isPostgres)
	_, prunesAccounting := store.(accountingPruneStore)
	require.False(t, prunesAccounting, "a shared store must not prune accounting from one replica's ledger")

	samples, err := store.LoadSamples(ctx)
	require.NoError(t, err)
	require.Equal(t, localSamples, samples)
	requests, err := store.LoadRequests(ctx)
	require.NoError(t, err)
	require.Equal(t, localRequests, requests)
	var importedSamples int
	require.NoError(t, postgres.pool.QueryRow(ctx, `SELECT count(*) FROM perf_host_samples`).Scan(&importedSamples))
	require.Equal(t, 2, importedSamples, "the sample past the walk's end is not imported")
	cached, found, err := store.FindAccountingRequest(ctx, "request-2", "escrow-2")
	require.NoError(t, err)
	require.True(t, found)
	require.Len(t, cached.Attempts, 1)
	again, err := store.BackfillLegacyEscrowSamples(ctx, "escrow-12", legacyPath, []string{"participant-legacy"})
	require.NoError(t, err)
	require.Empty(t, again, "the import keeps the backfill's dedupe key")
	ownFile, err := store.BackfillLegacyEscrowSamples(ctx, "escrow-own", localPath, []string{"participant-own"})
	require.NoError(t, err)
	require.Empty(t, ownFile, "the imported perf.db is never backfilled as an escrow's file")

	require.NoError(t, store.InsertSample(ctx, perfContractSample("participant-after-import", now)))
	require.NoError(t, store.InsertRequest(ctx, perfContractRequest(requestLogSize+2)))
	requestsBeforeReopen, err := store.LoadRequests(ctx)
	require.NoError(t, err)
	require.NoError(t, store.Close())

	reopened, err := NewPerfStore(ctx, storageDir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reopened.Close()) })
	requestsAfterReopen, err := reopened.LoadRequests(ctx)
	require.NoError(t, err)
	require.Equal(t, requestsBeforeReopen, requestsAfterReopen)
}

// Test flow:
//  1. Give two replicas their own perf.db, both holding accounting for the same request with different models, and one only replica B has.
//  2. Start replica A, then replica B, against one Postgres.
//  3. B's file is imported too: its own request is findable, and the request A imported first keeps A's row.
func TestNewPerfStoreImportsEachReplicasFileOnceAndKeepsRowsAlreadyThere(t *testing.T) {
	ctx := context.Background()
	startedAt := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	replicaDirs := map[string]string{"replica-a": t.TempDir(), "replica-b": t.TempDir()}
	for replica, storageDir := range replicaDirs {
		local, err := newSQLitePerfStore(filepath.Join(storageDir, "perf.db"))
		require.NoError(t, err)
		require.NoError(t, local.UpsertAccountingRequest(ctx, "request-shared", "escrow-1", "model-of-"+replica, startedAt))
		require.NoError(t, local.UpsertAccountingRequest(ctx, "request-of-"+replica, "escrow-1", "Qwen/Test", startedAt))
		require.NoError(t, local.Close())
	}
	t.Cleanup(setupPostgresContainer(t))
	t.Setenv(mode.EnvStorageMode, "postgres")

	for _, replica := range []string{"replica-a", "replica-b"} {
		store, err := NewPerfStore(ctx, replicaDirs[replica])
		require.NoError(t, err)
		require.NoError(t, store.Close())
	}

	store, err := NewPerfStore(ctx, replicaDirs["replica-b"])
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	_, found, err := store.FindAccountingRequest(ctx, "request-of-replica-b", "escrow-1")
	require.NoError(t, err)
	require.True(t, found, "a replica that starts after another one still imports its own perf.db")
	shared, found, err := store.FindAccountingRequest(ctx, "request-shared", "escrow-1")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "model-of-replica-a", shared.Model, "a row Postgres already holds is kept")
}

// Test flow:
//  1. On Postgres, write a live sample past the walk's end, a recent one, two requests more than the log reads, and accounting of an escrow no ledger retains.
//  2. Run one pass of the pruner the gateway builds for a shared store.
//  3. The old sample and the oldest requests are gone; the accounting is kept.
func TestPerfPrunerOnPostgresKeepsRequestAccounting(t *testing.T) {
	ctx := context.Background()
	store := newTestPostgresPerfStore(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	require.NoError(t, store.InsertSample(ctx, perfContractSample("participant-old", now.Add(-5*time.Hour))))
	require.NoError(t, store.InsertSample(ctx, perfContractSample("participant-recent", now.Add(-time.Minute))))
	for index := range requestLogSize + 2 {
		require.NoError(t, store.InsertRequest(ctx, perfContractRequest(index)))
	}
	startedAt := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	require.NoError(t, store.UpsertAccountingRequest(ctx, "request-1", "escrow-dropped", "Qwen/Test", startedAt))
	accountingStore, ownsAccounting := PerfStore(store).(accountingPruneStore)
	require.False(t, ownsAccounting)

	pruner := newPerfPruner(store, accountingStore, func(string) bool { return false }, func() uint64 { return 1 }, perfPrunerTiming{interval: time.Hour})
	result := pruner.runOnce(ctx)

	require.Equal(t, perfPruneResult{samples: 1, requestLog: 2}, result)
	_, found, err := store.FindAccountingRequest(ctx, "request-1", "escrow-dropped")
	require.NoError(t, err)
	require.True(t, found)
}

// Test flow:
//  1. Replica A serves on Postgres and records a recent sample and a recent request.
//  2. Replica B starts with a perf.db holding a sample a little inside the walk's stop line and a full request log, and imports it.
//  3. A prune a few minutes later ends the walk at B's imported sample, below A's: A's recent sample survives it, and the request log still ends with A's request.
func TestNewPerfStoreImportIntoAServingDatabaseKeepsTheServingReplicasHistory(t *testing.T) {
	ctx := context.Background()
	t.Cleanup(setupPostgresContainer(t))
	t.Setenv(mode.EnvStorageMode, "postgres")
	now := time.Now().UTC().Truncate(time.Millisecond)

	replicaA, err := NewPerfStore(ctx, t.TempDir())
	require.NoError(t, err)
	recentOfA := perfContractSample("participant-of-a", now.Add(-time.Minute))
	require.NoError(t, replicaA.InsertSample(ctx, recentOfA))
	requestOfA := perfContractRequest(requestLogSize + 10)
	requestOfA.Timestamp = now
	require.NoError(t, replicaA.InsertRequest(ctx, requestOfA))
	require.NoError(t, replicaA.Close())

	storageDirOfB := t.TempDir()
	localOfB, err := newSQLitePerfStore(filepath.Join(storageDirOfB, "perf.db"))
	require.NoError(t, err)
	_, stopBefore := sampleWindow(now)
	require.NoError(t, localOfB.InsertSample(ctx, perfContractSample("participant-of-b", stopBefore.Add(2*time.Minute))))
	for index := range requestLogSize {
		require.NoError(t, localOfB.InsertRequest(ctx, perfContractRequest(index)))
	}
	require.NoError(t, localOfB.Close())
	replicaB, err := NewPerfStore(ctx, storageDirOfB)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, replicaB.Close()) })

	boundary, found, err := findHostSampleBoundary(ctx, replicaB, now.Add(5*time.Minute), func() bool { return true })
	require.NoError(t, err)
	require.True(t, found, "the imported sample has fallen past the stop line and ends the walk")
	_, err = replicaB.deleteHostSamplesUpTo(ctx, boundary, perfPruneBatchSize)
	require.NoError(t, err)
	samples, err := replicaB.LoadSamples(ctx)
	require.NoError(t, err)
	require.Contains(t, samples, recentOfA)
	requests, err := replicaB.LoadRequests(ctx)
	require.NoError(t, err)
	require.Equal(t, requestOfA, requests[len(requests)-1], "the request log still ends with the newest request")
}

// Test flow:
//  1. Replica B serves on an empty Postgres; replica A has more history samples and request log rows than one import page holds.
//  2. A imports while B writes a live sample and a live request between A's import pages.
//  3. B's writes stay the newest rows: the startup walk and the request log end with them, not with A's older history.
func TestNewPerfStoreImportKeepsLiveWritesNewerThanTheImportedHistory(t *testing.T) {
	ctx := context.Background()
	t.Cleanup(setupPostgresContainer(t))
	t.Setenv(mode.EnvStorageMode, "postgres")
	now := time.Now().UTC().Truncate(time.Millisecond)

	replicaB, err := NewPerfStore(ctx, t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, replicaB.Close()) })

	storageDirOfA := t.TempDir()
	localOfA, err := newSQLitePerfStore(filepath.Join(storageDirOfA, "perf.db"))
	require.NoError(t, err)
	historyStart := now.Add(-30 * time.Minute)
	for index := range perfImportPageSize + 5 {
		require.NoError(t, localOfA.InsertSample(ctx, perfContractSample("participant-of-a", historyStart.Add(time.Duration(index)*time.Millisecond))))
	}
	for index := range requestLogSize {
		require.NoError(t, localOfA.InsertRequest(ctx, perfContractRequest(index)))
	}
	require.NoError(t, localOfA.Close())

	liveSampleOfB := perfContractSample("participant-of-b", now)
	liveRequestOfB := perfContractRequest(requestLogSize + 10)
	liveRequestOfB.Timestamp = now
	writtenDuringImport := map[string]bool{}
	testHookPerfImportPageCopied = func(tableName string) {
		if writtenDuringImport[tableName] {
			return
		}
		writtenDuringImport[tableName] = true
		switch tableName {
		case "perf_host_samples":
			require.NoError(t, replicaB.InsertSample(ctx, liveSampleOfB))
		case "perf_request_log":
			require.NoError(t, replicaB.InsertRequest(ctx, liveRequestOfB))
		}
	}
	t.Cleanup(func() { testHookPerfImportPageCopied = nil })
	replicaA, err := NewPerfStore(ctx, storageDirOfA)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, replicaA.Close()) })
	require.True(t, writtenDuringImport["perf_host_samples"])
	require.True(t, writtenDuringImport["perf_request_log"])

	samples, err := replicaA.LoadSamples(ctx)
	require.NoError(t, err)
	require.Equal(t, liveSampleOfB, samples[len(samples)-1], "a live sample written during the import must stay above the imported history")
	requests, err := replicaA.LoadRequests(ctx)
	require.NoError(t, err)
	require.Equal(t, liveRequestOfB, requests[len(requests)-1], "a live request written during the import must stay the newest")
}

// Test flow:
//  1. Replica A has served long enough to hold a live sample past the walk's stop line and a full request log.
//  2. Replica B imports a perf.db holding a recent sample and request log rows.
//  3. B's samples and request log rows are not copied, since the served walk and log would never read them; B's accounting still is.
func TestNewPerfStoreImportSkipsHistoryAServedDatabaseWouldNeverRead(t *testing.T) {
	ctx := context.Background()
	t.Cleanup(setupPostgresContainer(t))
	t.Setenv(mode.EnvStorageMode, "postgres")
	now := time.Now().UTC().Truncate(time.Millisecond)

	replicaA, err := NewPerfStore(ctx, t.TempDir())
	require.NoError(t, err)
	require.NoError(t, replicaA.InsertSample(ctx, perfContractSample("participant-of-a", now.Add(-5*time.Hour))))
	require.NoError(t, replicaA.InsertSample(ctx, perfContractSample("participant-of-a", now.Add(-time.Minute))))
	for index := range requestLogSize {
		require.NoError(t, replicaA.InsertRequest(ctx, perfContractRequest(index)))
	}
	require.NoError(t, replicaA.Close())

	storageDirOfB := t.TempDir()
	localOfB, err := newSQLitePerfStore(filepath.Join(storageDirOfB, "perf.db"))
	require.NoError(t, err)
	require.NoError(t, localOfB.InsertSample(ctx, perfContractSample("participant-of-b", now.Add(-2*time.Minute))))
	require.NoError(t, localOfB.InsertRequest(ctx, perfContractRequest(requestLogSize+1)))
	startedAt := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	require.NoError(t, localOfB.UpsertAccountingRequest(ctx, "request-of-b", "escrow-1", "Qwen/Test", startedAt))
	require.NoError(t, localOfB.Close())

	replicaB, err := NewPerfStore(ctx, storageDirOfB)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, replicaB.Close()) })
	postgres := replicaB.(*postgresPerfStore)
	var samplesOfB, requestRows int
	require.NoError(t, postgres.pool.QueryRow(ctx, `SELECT count(*) FROM perf_host_samples WHERE participant_key = 'participant-of-b'`).Scan(&samplesOfB))
	require.NoError(t, postgres.pool.QueryRow(ctx, `SELECT count(*) FROM perf_request_log`).Scan(&requestRows))
	require.Zero(t, samplesOfB)
	require.Equal(t, requestLogSize, requestRows)
	_, found, err := replicaB.FindAccountingRequest(ctx, "request-of-b", "escrow-1")
	require.NoError(t, err)
	require.True(t, found)
}
