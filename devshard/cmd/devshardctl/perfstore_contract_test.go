package main

import (
	"context"
	"math"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func newTestSQLitePerfStore(t *testing.T) *sqlitePerfStore {
	t.Helper()
	store, err := newSQLitePerfStore(filepath.Join(t.TempDir(), "perf.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	return store
}

func newTestPostgresPerfStore(t *testing.T) *postgresPerfStore {
	t.Helper()
	t.Cleanup(setupPostgresContainer(t))
	store, err := newPostgresPerfStore(context.Background(), "")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	return store
}

func forEachPerfStoreBackend(t *testing.T, run func(t *testing.T, store PerfStore)) {
	t.Run("sqlite", func(t *testing.T) { run(t, newTestSQLitePerfStore(t)) })
	t.Run("postgres", func(t *testing.T) { run(t, newTestPostgresPerfStore(t)) })
}

func perfContractSample(participantKey string, sentAt time.Time) RequestSample {
	return RequestSample{
		HostIdx:        1,
		ParticipantKey: participantKey,
		Responsive:     true,
		SendTime:       sentAt,
		ReceiptTime:    sentAt.Add(200 * time.Millisecond),
		FirstToken:     sentAt.Add(400 * time.Millisecond),
		TotalTime:      1500 * time.Millisecond,
		InputTokens:    321,
	}
}

func perfContractRequest(index int) RequestRecord {
	return RequestRecord{
		Timestamp:     time.Date(2026, 10, 1, 0, 0, index, 0, time.UTC),
		Model:         "Qwen/Test",
		InputTokens:   uint64(100 + index),
		WinnerHostIdx: index % 3,
		WinnerNonce:   uint64(1000 + index),
		Decision:      "primary",
		Hosts:         []HostInvolvement{{HostIdx: index % 3, Nonce: uint64(1000 + index)}},
	}
}

// Test flow:
//  1. Write a sample past the walk's stop line, one inside the slack hour, a recent one without a participant key and a recent keyed one.
//  2. Load samples: only the recent keyed sample comes back, with its fields intact.
//  3. Write three requests and load them back oldest first with their hosts.
func TestPerfStoreLoadsTheSampleWindowAndTheRequestLog(t *testing.T) {
	forEachPerfStoreBackend(t, func(t *testing.T, store PerfStore) {
		ctx := context.Background()
		now := time.Now().UTC().Truncate(time.Millisecond)
		for _, sample := range []RequestSample{
			perfContractSample("participant-old", now.Add(-5*time.Hour)),
			perfContractSample("participant-slack", now.Add(-210*time.Minute)),
			perfContractSample("", now.Add(-time.Minute)),
			perfContractSample("participant-recent", now.Add(-time.Minute)),
		} {
			require.NoError(t, store.InsertSample(ctx, sample))
		}

		samples, err := store.LoadSamples(ctx)
		require.NoError(t, err)
		require.Len(t, samples, 1)
		require.Equal(t, perfContractSample("participant-recent", now.Add(-time.Minute)), samples[0])

		for index := range 3 {
			require.NoError(t, store.InsertRequest(ctx, perfContractRequest(index)))
		}
		records, err := store.LoadRequests(ctx)
		require.NoError(t, err)
		require.Equal(t, []RequestRecord{perfContractRequest(0), perfContractRequest(1), perfContractRequest(2)}, records)
	})
}

// Test flow:
//  1. Start a request, record two attempts, then complete it with the second nonce winning.
//  2. Find it: the winner flag follows the completion, not the attempt writes.
//  3. Alias a cached request to it and find the cached one: it carries the source's attempts under its own ids.
//  4. A request aliased to itself is not stored.
func TestPerfStoreResolvesAccountingWinnersAndCachedAliases(t *testing.T) {
	forEachPerfStoreBackend(t, func(t *testing.T, store PerfStore) {
		ctx := context.Background()
		startedAt := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
		require.NoError(t, store.UpsertAccountingRequest(ctx, "request-1", "escrow-1", "Qwen/Test", startedAt))
		require.NoError(t, store.UpsertAccountingAttempt(ctx, RequestAccountingAttempt{RequestID: "request-1", EscrowID: "escrow-1", Nonce: 7, HostIdx: 0, ParticipantKey: "participant-a", Winner: true, CreatedAt: startedAt}))
		require.NoError(t, store.UpsertAccountingAttempt(ctx, RequestAccountingAttempt{RequestID: "request-1", EscrowID: "escrow-1", Nonce: 8, HostIdx: 1, ParticipantKey: "participant-b", Probe: true, CreatedAt: startedAt}))
		require.NoError(t, store.CompleteAccountingRequest(ctx, "request-1", "escrow-1", 8, "fallback", "", startedAt.Add(time.Second)))

		record, found, err := store.FindAccountingRequest(ctx, "request-1", "escrow-1")
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, "settled", record.Outcome)
		require.Equal(t, "fallback", record.Decision)
		require.Equal(t, uint64(8), record.WinnerNonce)
		require.Equal(t, startedAt, record.StartedAt)
		require.Len(t, record.Attempts, 2)
		require.False(t, record.Attempts[0].Winner, "nonce 7 lost when the completion named nonce 8")
		require.True(t, record.Attempts[1].Winner)
		require.True(t, record.Attempts[1].Probe)

		require.NoError(t, store.UpsertAccountingAlias(ctx, "request-2", "escrow-2", "request-1", "escrow-1", "cache_hit", startedAt))
		cached, found, err := store.FindAccountingRequest(ctx, "request-2", "escrow-2")
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, "cached", cached.Outcome)
		require.Equal(t, "cache_hit", cached.Decision)
		require.Equal(t, "request-1", cached.CachedFromRequestID)
		require.Equal(t, "escrow-1", cached.CachedFromEscrowID)
		require.Len(t, cached.Attempts, 2)

		require.NoError(t, store.UpsertAccountingAlias(ctx, "request-3", "escrow-3", "request-3", "escrow-3", "cache_hit", startedAt))
		_, found, err = store.FindAccountingRequest(ctx, "request-3", "escrow-3")
		require.NoError(t, err)
		require.False(t, found)
	})
}

// Test flow:
//  1. Write a per-escrow perf file with samples for a host the escrow can name and one it cannot.
//  2. Backfill it: only the named host's sample is inserted and returned.
//  3. Backfill it again: nothing is inserted twice.
func TestPerfStoreBackfillsLegacyEscrowSamplesOnce(t *testing.T) {
	forEachPerfStoreBackend(t, func(t *testing.T, store PerfStore) {
		ctx := context.Background()
		legacyPath := filepath.Join(t.TempDir(), "escrow-12-state.db")
		legacy, err := newSQLitePerfStore(legacyPath)
		require.NoError(t, err)
		sentAt := time.Now().UTC().Add(-time.Minute).Truncate(time.Millisecond)
		named := perfContractSample("", sentAt)
		named.HostIdx = 0
		unnamed := perfContractSample("", sentAt)
		unnamed.HostIdx = 5
		require.NoError(t, legacy.InsertSample(ctx, named))
		require.NoError(t, legacy.InsertSample(ctx, unnamed))
		require.NoError(t, legacy.Close())

		inserted, err := store.BackfillLegacyEscrowSamples(ctx, "escrow-12", legacyPath, []string{"participant-a"})
		require.NoError(t, err)
		require.Len(t, inserted, 1)
		require.Equal(t, "participant-a", inserted[0].ParticipantKey)

		again, err := store.BackfillLegacyEscrowSamples(ctx, "escrow-12", legacyPath, []string{"participant-a"})
		require.NoError(t, err)
		require.Empty(t, again)
	})
}

// Test flow:
//  1. Write a per-escrow perf file holding accounting for two escrows.
//  2. Import one escrow: only its request and attempt are counted and become findable.
func TestPerfStoreImportsOneEscrowsAccountingFromAFile(t *testing.T) {
	forEachPerfStoreBackend(t, func(t *testing.T, store PerfStore) {
		ctx := context.Background()
		sourcePath := filepath.Join(t.TempDir(), "escrow-perf.db")
		source, err := newSQLitePerfStore(sourcePath)
		require.NoError(t, err)
		startedAt := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
		for _, escrowID := range []string{"escrow-imported", "escrow-other"} {
			require.NoError(t, source.UpsertAccountingRequest(ctx, "request-1", escrowID, "Qwen/Test", startedAt))
			require.NoError(t, source.UpsertAccountingAttempt(ctx, RequestAccountingAttempt{RequestID: "request-1", EscrowID: escrowID, Nonce: 3, CreatedAt: startedAt}))
		}
		require.NoError(t, source.Close())

		requests, attempts, err := store.ImportRequestAccounting(ctx, sourcePath, "escrow-imported")
		require.NoError(t, err)
		require.Equal(t, int64(1), requests)
		require.Equal(t, int64(1), attempts)

		record, found, err := store.FindAccountingRequest(ctx, "request-1", "escrow-imported")
		require.NoError(t, err)
		require.True(t, found)
		require.Len(t, record.Attempts, 1)
		_, found, err = store.FindAccountingRequest(ctx, "request-1", "escrow-other")
		require.NoError(t, err)
		require.False(t, found)
	})
}

// Test flow:
//  1. Write a live sample past the walk's stop line, then a recent one; find the walk's boundary and delete up to it.
//  2. Write three requests more than the log reads; delete up to its retention boundary.
//  3. What startup loads is unchanged.
func TestPerfStorePruneDeletesOnlyWhatStartupNoLongerReads(t *testing.T) {
	forEachPerfStoreBackend(t, func(t *testing.T, store PerfStore) {
		ctx := context.Background()
		now := time.Now().UTC().Truncate(time.Millisecond)
		require.NoError(t, store.InsertSample(ctx, perfContractSample("participant-old", now.Add(-5*time.Hour))))
		require.NoError(t, store.InsertSample(ctx, perfContractSample("participant-recent", now.Add(-time.Minute))))
		samplesBefore, err := store.LoadSamples(ctx)
		require.NoError(t, err)

		_, stopBefore := sampleWindow(now)
		boundary, _, _, err := store.hostSampleBoundaryBatch(ctx, stopBefore, math.MaxInt64, 10)
		require.NoError(t, err)
		require.Positive(t, boundary)
		deletedSamples, err := store.deleteHostSamplesUpTo(ctx, boundary, 10)
		require.NoError(t, err)
		require.Equal(t, int64(1), deletedSamples)
		samplesAfter, err := store.LoadSamples(ctx)
		require.NoError(t, err)
		require.Equal(t, samplesBefore, samplesAfter)

		for index := range requestLogSize + 3 {
			require.NoError(t, store.InsertRequest(ctx, perfContractRequest(index)))
		}
		requestsBefore, err := store.LoadRequests(ctx)
		require.NoError(t, err)
		logBoundary, err := store.requestLogRetentionBoundary(ctx)
		require.NoError(t, err)
		deletedRequests, err := store.deleteRequestLogUpTo(ctx, logBoundary, 10)
		require.NoError(t, err)
		require.Equal(t, int64(3), deletedRequests)
		requestsAfter, err := store.LoadRequests(ctx)
		require.NoError(t, err)
		require.Equal(t, requestsBefore, requestsAfter)
	})
}

// Test flow:
//  1. Start a request with a model, then upsert it again without one: the model stays.
//  2. Record a winning attempt, then upsert it again as not winning: the win stays until a completion names another nonce.
//  3. Alias a request to one source, then to another: the alias follows the latest source.
func TestPerfStoreUpsertsKeepWhatALaterWriteMustNotErase(t *testing.T) {
	forEachPerfStoreBackend(t, func(t *testing.T, store PerfStore) {
		ctx := context.Background()
		startedAt := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
		for _, requestID := range []string{"request-1", "request-2"} {
			require.NoError(t, store.UpsertAccountingRequest(ctx, requestID, "escrow-1", "Qwen/Test", startedAt))
		}
		require.NoError(t, store.UpsertAccountingRequest(ctx, "request-1", "escrow-1", "", startedAt))
		require.NoError(t, store.UpsertAccountingAttempt(ctx, RequestAccountingAttempt{RequestID: "request-1", EscrowID: "escrow-1", Nonce: 4, Winner: true, CreatedAt: startedAt}))
		require.NoError(t, store.UpsertAccountingAttempt(ctx, RequestAccountingAttempt{RequestID: "request-1", EscrowID: "escrow-1", Nonce: 4, CreatedAt: startedAt}))

		record, found, err := store.FindAccountingRequest(ctx, "request-1", "escrow-1")
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, "Qwen/Test", record.Model)
		require.Len(t, record.Attempts, 1)
		require.True(t, record.Attempts[0].Winner)

		require.NoError(t, store.UpsertAccountingAlias(ctx, "request-3", "escrow-3", "request-1", "escrow-1", "cache_hit", startedAt))
		require.NoError(t, store.UpsertAccountingAlias(ctx, "request-3", "escrow-3", "request-2", "escrow-1", "cache_hit", startedAt))
		cached, found, err := store.FindAccountingRequest(ctx, "request-3", "escrow-3")
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, "request-2", cached.CachedFromRequestID)
	})
}

// Test flow:
//  1. Write more recent keyed samples than one page of the walk holds.
//  2. Load samples: every one comes back, oldest first.
func TestPerfStoreLoadsSamplesAcrossWalkPages(t *testing.T) {
	forEachPerfStoreBackend(t, func(t *testing.T, store PerfStore) {
		ctx := context.Background()
		firstSentAt := time.Now().UTC().Add(-30 * time.Minute).Truncate(time.Millisecond)
		total := perfSampleLoadPageSize + 3
		for index := range total {
			require.NoError(t, store.InsertSample(ctx, perfContractSample("participant-a", firstSentAt.Add(time.Duration(index)*time.Millisecond))))
		}

		samples, err := store.LoadSamples(ctx)
		require.NoError(t, err)
		require.Len(t, samples, total)
		require.Equal(t, firstSentAt, samples[0].SendTime)
		require.Equal(t, firstSentAt.Add(time.Duration(total-1)*time.Millisecond), samples[total-1].SendTime)
	})
}
