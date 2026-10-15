package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func insertPerfSampleRow(t *testing.T, store *sqlitePerfStore, participant string, sentAt time.Time, sourceEscrow string, sourceSampleID int64) {
	t.Helper()
	var sourceID any
	if sourceEscrow != "" {
		sourceID = sourceSampleID
	}
	_, err := store.db.Exec(`INSERT INTO perf_host_samples
		(host_idx, participant_key, responsive, send_time, receipt_time, first_token, total_time_ms, input_tokens, source_escrow, source_sample_id)
		VALUES (0, ?, 1, ?, '', '', 1000, 10, ?, ?)`, participant, sentAt.Format(time.RFC3339Nano), sourceEscrow, sourceID)
	require.NoError(t, err)
}

// Test flow:
//  1. Store a long live history older than the window, then in id order: a sample inside the window, a long request that started just before the window but finished later, legacy samples backfilled at high ids inside and outside the window, and a fresh sample.
//  2. Load the samples.
//  3. Require exactly the samples inside the window, oldest first, so neither the late finisher nor a backfilled old sample cuts the load short.
func TestLoadSamplesReturnsTheWindowAcrossLateFinishersAndBackfilledRows(t *testing.T) {
	store, err := newSQLitePerfStore(filepath.Join(t.TempDir(), "perf.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	now := time.Now()
	cutoff := now.Add(-2*ParticipantPerfWindow - time.Hour)
	for index := range 50 {
		insertPerfSampleRow(t, store, "old-history", now.Add(-48*time.Hour+time.Duration(index)*time.Minute), "", 0)
	}
	insertPerfSampleRow(t, store, "inside-window", cutoff.Add(10*time.Minute), "", 0)
	insertPerfSampleRow(t, store, "late-finisher", cutoff.Add(-10*time.Minute), "", 0)
	insertPerfSampleRow(t, store, "backfilled-inside", now.Add(-30*time.Minute), "legacy-escrow", 1)
	insertPerfSampleRow(t, store, "backfilled-old", now.Add(-30*time.Hour), "legacy-escrow", 2)
	insertPerfSampleRow(t, store, "fresh", now.Add(-time.Minute), "", 0)

	samples, err := store.LoadSamples(context.Background())
	require.NoError(t, err)

	participants := make([]string, 0, len(samples))
	for _, sample := range samples {
		participants = append(participants, sample.ParticipantKey)
	}
	require.Equal(t, []string{"inside-window", "backfilled-inside", "fresh"}, participants)
}
