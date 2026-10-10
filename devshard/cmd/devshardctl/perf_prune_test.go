package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func openPruneTestStore(t *testing.T) *sqlitePerfStore {
	t.Helper()
	store, err := newSQLitePerfStore(filepath.Join(t.TempDir(), "perf.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func countRows(t *testing.T, store *sqlitePerfStore, query string, args ...any) int {
	t.Helper()
	var count int
	require.NoError(t, store.db.QueryRow(query, args...).Scan(&count))
	return count
}

func insertAccountingRows(t *testing.T, store *sqlitePerfStore, requestID, escrowID string) {
	t.Helper()
	now := time.Now()
	require.NoError(t, store.UpsertAccountingRequest(context.Background(), requestID, escrowID, "m", now))
	require.NoError(t, store.UpsertAccountingAttempt(context.Background(), RequestAccountingAttempt{RequestID: requestID, EscrowID: escrowID, Nonce: 1, CreatedAt: now}))
	require.NoError(t, store.UpsertAccountingAlias(context.Background(), requestID, escrowID, "source-"+requestID, escrowID, "retry", now))
}

func accountingRowsOf(t *testing.T, store *sqlitePerfStore, escrowID string) int {
	t.Helper()
	total := 0
	for _, table := range []string{"request_accounting", "request_accounting_attempts", "request_accounting_aliases"} {
		total += countRows(t, store, "SELECT count(*) FROM "+table+" WHERE escrow_id = ?", escrowID)
	}
	return total
}

func newTestPerfPruner(store *sqlitePerfStore, retainsEscrow func(string) bool, currentEpoch func() uint64) *perfPruner {
	return newPerfPruner(store, store, retainsEscrow, currentEpoch, perfPrunerTiming{interval: time.Hour})
}

// Test flow:
//  1. Store a long live history before the window, then the window's edge cases: a late finisher, backfilled rows inside and outside it, fresh samples.
//  2. Run one prune pass.
//  3. Require startup to load exactly what it loaded before, and the history below the window to be gone.
func TestPerfPrunerDropsSampleHistoryWithoutChangingWhatStartupLoads(t *testing.T) {
	store := openPruneTestStore(t)
	now := time.Now()
	cutoff := now.Add(-2*ParticipantPerfWindow - time.Hour)
	for index := range 50 {
		insertPerfSampleRow(t, store, "old-history", now.Add(-48*time.Hour+time.Duration(index)*time.Minute), "", 0)
	}
	insertPerfSampleRow(t, store, "inside-window", cutoff.Add(10*time.Minute), "", 0)
	insertPerfSampleRow(t, store, "late-finisher", cutoff.Add(-10*time.Minute), "", 0)
	insertPerfSampleRow(t, store, "backfilled-inside", now.Add(-30*time.Minute), "legacy-escrow", 1)
	insertPerfSampleRow(t, store, "fresh", now.Add(-time.Minute), "", 0)
	before, err := store.LoadSamples(context.Background())
	require.NoError(t, err)

	result := newTestPerfPruner(store, nil, nil).runOnce(context.Background())

	after, err := store.LoadSamples(context.Background())
	require.NoError(t, err)
	require.Equal(t, before, after)
	require.Zero(t, countRows(t, store, "SELECT count(*) FROM perf_host_samples WHERE participant_key = 'old-history'"))
	require.Equal(t, int64(50), result.samples)
}

// Test flow:
//  1. Store more request log rows than startup reads.
//  2. Run one prune pass.
//  3. Require exactly the newest requestLogSize rows to remain and startup to load the same records.
func TestPerfPrunerKeepsOnlyTheRequestLogStartupReads(t *testing.T) {
	store := openPruneTestStore(t)
	_, err := store.db.Exec("BEGIN")
	require.NoError(t, err)
	for index := range requestLogSize + 300 {
		require.NoError(t, store.InsertRequest(context.Background(), RequestRecord{Timestamp: time.Unix(int64(index), 0), WinnerNonce: uint64(index)}))
	}
	_, err = store.db.Exec("COMMIT")
	require.NoError(t, err)
	before, err := store.LoadRequests(context.Background())
	require.NoError(t, err)

	result := newTestPerfPruner(store, nil, nil).runOnce(context.Background())

	after, err := store.LoadRequests(context.Background())
	require.NoError(t, err)
	require.Equal(t, before, after)
	require.Equal(t, requestLogSize, countRows(t, store, "SELECT count(*) FROM perf_request_log"))
	require.Equal(t, int64(300), result.requestLog)
}

// Test flow:
//  1. Store request accounting for an escrow the ledger retains and one it no longer does.
//  2. Run one prune pass without a retention rule, then one with it.
//  3. Require nothing to go without the rule, and with it only the expired escrow's rows in every accounting table.
func TestPerfPrunerDropsAccountingOnlyForEscrowsTheLedgerNoLongerRetains(t *testing.T) {
	store := openPruneTestStore(t)
	insertAccountingRows(t, store, "chatcmpl-kept", "kept")
	insertAccountingRows(t, store, "chatcmpl-expired", "expired")

	newTestPerfPruner(store, nil, func() uint64 { return 5 }).runOnce(context.Background())
	require.Equal(t, 3, accountingRowsOf(t, store, "expired"))

	result := newTestPerfPruner(store, func(escrowID string) bool { return escrowID == "kept" }, func() uint64 { return 5 }).runOnce(context.Background())

	require.Zero(t, accountingRowsOf(t, store, "expired"))
	require.Equal(t, 3, accountingRowsOf(t, store, "kept"))
	require.Equal(t, int64(3), result.accounting)
}

// Test flow:
//  1. Prune accounting once in an epoch.
//  2. Add accounting for another expired escrow and prune again in the same epoch, then in the next one.
//  3. Require the second pass to leave it and the third to drop it, so the full walk runs once per epoch.
func TestPerfPrunerWalksAccountingOncePerEpoch(t *testing.T) {
	store := openPruneTestStore(t)
	epoch := uint64(5)
	pruner := newTestPerfPruner(store, func(string) bool { return false }, func() uint64 { return epoch })
	pruner.runOnce(context.Background())

	insertAccountingRows(t, store, "chatcmpl-late", "late")
	pruner.runOnce(context.Background())
	require.Equal(t, 3, accountingRowsOf(t, store, "late"))

	epoch = 6
	pruner.runOnce(context.Background())
	require.Zero(t, accountingRowsOf(t, store, "late"))
}

// Test flow:
//  1. Start a pruner whose pause between batches is an hour, over a history several batches long, and wait until its first batch is gone.
//  2. Stop it while it waits out the pause.
//  3. Require the stop to return promptly.
func TestPerfPrunerStopsWhileItWaits(t *testing.T) {
	store := openPruneTestStore(t)
	_, err := store.db.Exec("BEGIN")
	require.NoError(t, err)
	old := time.Now().Add(-48 * time.Hour)
	for index := range 3 * perfPruneBatchSize {
		insertPerfSampleRow(t, store, "old-history", old.Add(time.Duration(index)*time.Millisecond), "", 0)
	}
	insertPerfSampleRow(t, store, "fresh", time.Now(), "", 0)
	_, err = store.db.Exec("COMMIT")
	require.NoError(t, err)
	pruner := newPerfPruner(store, store, nil, nil, perfPrunerTiming{startDelay: 0, interval: time.Hour, pause: time.Hour})
	pruner.start()
	require.Eventually(t, func() bool {
		return countRows(t, store, "SELECT count(*) FROM perf_host_samples") <= 2*perfPruneBatchSize+1
	}, 5*time.Second, 5*time.Millisecond, "the first batch was never deleted")

	stopped := make(chan struct{})
	go func() {
		pruner.stopAndWait()
		close(stopped)
	}()

	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("stopAndWait did not return within 2s")
	}
}

// Test flow:
//  1. Store accounting for an expired escrow and give the pruner a retention check that itself uses the store, as gateway paths holding g.mu do.
//  2. Run one prune pass with a deadline.
//  3. Require it to finish and drop the expired rows, so the check never runs while a cursor holds the store's only connection.
func TestPerfPrunerRetentionCheckCanUseTheStore(t *testing.T) {
	store := openPruneTestStore(t)
	insertAccountingRows(t, store, "chatcmpl-expired", "expired")
	retainsEscrow := func(escrowID string) bool {
		var count int
		require.NoError(t, store.db.QueryRow("SELECT count(*) FROM perf_request_log").Scan(&count))
		return escrowID != "expired"
	}
	pruner := newTestPerfPruner(store, retainsEscrow, func() uint64 { return 5 })

	finished := make(chan struct{})
	go func() {
		pruner.runOnce(context.Background())
		close(finished)
	}()

	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("runOnce did not finish within 5s: the retention check waited on the connection its own cursor holds")
	}
	require.Zero(t, accountingRowsOf(t, store, "expired"))
}

// Test flow:
//  1. Store a history older than the window, then more samples inside the window than one prune batch holds.
//  2. Run one prune pass.
//  3. Require every window sample to stay and the whole older history to go, so the boundary search carries across batches.
func TestPerfPrunerFindsTheSampleBoundaryPastTheFirstBatch(t *testing.T) {
	store := openPruneTestStore(t)
	now := time.Now()
	_, err := store.db.Exec("BEGIN")
	require.NoError(t, err)
	for index := range 100 {
		insertPerfSampleRow(t, store, "old-history", now.Add(-48*time.Hour+time.Duration(index)*time.Second), "", 0)
	}
	windowSamples := perfPruneBatchSize + 500
	for index := range windowSamples {
		insertPerfSampleRow(t, store, "inside-window", now.Add(-time.Hour+time.Duration(index)*time.Millisecond), "", 0)
	}
	_, err = store.db.Exec("COMMIT")
	require.NoError(t, err)

	result := newTestPerfPruner(store, nil, nil).runOnce(context.Background())

	require.Equal(t, int64(100), result.samples)
	require.Zero(t, countRows(t, store, "SELECT count(*) FROM perf_host_samples WHERE participant_key = 'old-history'"))
	require.Equal(t, windowSamples, countRows(t, store, "SELECT count(*) FROM perf_host_samples WHERE participant_key = 'inside-window'"))
}
