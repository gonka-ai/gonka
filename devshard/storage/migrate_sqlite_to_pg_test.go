package storage

import (
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/types"
)

func TestMigrateSQLiteSessions_RoundTrip(t *testing.T) {
	srcDir := t.TempDir()
	src, err := NewSQLite(srcDir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = src.Close() })

	require.NoError(t, src.CreateSession(paramsForEpoch("esc-a", 3)))
	require.NoError(t, src.AppendDiff("esc-a", makeDiffRecord(1)))
	require.NoError(t, src.AppendDiff("esc-a", makeDiffRecord(2)))
	require.NoError(t, src.MarkFinalized("esc-a", 2))
	require.NoError(t, src.SaveSnapshot("esc-a", 2, []byte("snap-a")))

	require.NoError(t, src.CreateSession(paramsForEpoch("esc-b", 4)))
	require.NoError(t, src.MarkSettled("esc-b"))

	// Force multiple workers even for two escrows.
	t.Setenv("DEVSHARD_MIGRATE_WORKERS", "4")

	dest := NewMemory()
	n, err := MigrateSQLiteSessions(src, dest)
	require.NoError(t, err)
	require.Equal(t, 2, n)

	metaA, err := dest.GetSessionMeta("esc-a")
	require.NoError(t, err)
	require.Equal(t, uint64(3), metaA.EpochID)
	require.Equal(t, uint64(2), metaA.LatestNonce)
	require.Equal(t, uint64(2), metaA.LastFinalized)

	diffs, err := dest.GetDiffs("esc-a", 1, 2)
	require.NoError(t, err)
	require.Len(t, diffs, 2)

	snapNonce, snapData, err := dest.LoadSnapshot("esc-a")
	require.NoError(t, err)
	require.Equal(t, uint64(2), snapNonce)
	require.Equal(t, []byte("snap-a"), snapData)

	metaB, err := dest.GetSessionMeta("esc-b")
	require.NoError(t, err)
	require.Equal(t, "settled", metaB.Status)

	// Idempotent second pass.
	n, err = MigrateSQLiteSessions(src, dest)
	require.NoError(t, err)
	require.Equal(t, 2, n)
}

func TestMigrateWorkerCount(t *testing.T) {
	t.Setenv("DEVSHARD_MIGRATE_WORKERS", "")
	require.Equal(t, defaultMigrateWorkers, migrateWorkerCount())

	t.Setenv("DEVSHARD_MIGRATE_WORKERS", "8")
	require.Equal(t, 8, migrateWorkerCount())

	t.Setenv("DEVSHARD_MIGRATE_WORKERS", "0")
	require.Equal(t, defaultMigrateWorkers, migrateWorkerCount())

	t.Setenv("DEVSHARD_MIGRATE_WORKERS", "nope")
	require.Equal(t, defaultMigrateWorkers, migrateWorkerCount())
}

func TestMemoryAppendDiffs(t *testing.T) {
	dest := NewMemory()
	require.NoError(t, dest.CreateSession(paramsForEpoch("batch", 1)))
	require.NoError(t, dest.AppendDiffs("batch", []types.DiffRecord{
		makeDiffRecord(1),
		makeDiffRecord(2),
		makeDiffRecord(3),
	}))
	diffs, err := dest.GetDiffs("batch", 1, 3)
	require.NoError(t, err)
	require.Len(t, diffs, 3)
}

func TestMigrateDiffChunkSize(t *testing.T) {
	t.Setenv("DEVSHARD_MIGRATE_DIFF_CHUNK", "")
	require.Equal(t, uint64(defaultMigrateDiffChunk), migrateDiffChunkSize())

	t.Setenv("DEVSHARD_MIGRATE_DIFF_CHUNK", "16")
	require.Equal(t, uint64(16), migrateDiffChunkSize())

	t.Setenv("DEVSHARD_MIGRATE_DIFF_CHUNK", "0")
	require.Equal(t, uint64(defaultMigrateDiffChunk), migrateDiffChunkSize())
}

func TestMigrateSQLiteSessions_ChunkedDiffs(t *testing.T) {
	srcDir := t.TempDir()
	src, err := NewSQLite(srcDir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = src.Close() })

	require.NoError(t, src.CreateSession(paramsForEpoch("chunky", 1)))
	for n := uint64(1); n <= 5; n++ {
		require.NoError(t, src.AppendDiff("chunky", makeDiffRecord(n)))
	}

	t.Setenv("DEVSHARD_MIGRATE_DIFF_CHUNK", "2")
	dest := NewMemory()
	n, err := MigrateSQLiteSessions(src, dest)
	require.NoError(t, err)
	require.Equal(t, 1, n)

	diffs, err := dest.GetDiffs("chunky", 1, 5)
	require.NoError(t, err)
	require.Len(t, diffs, 5)

	// Resume mid-migrate: dest already has first 3 nonces.
	partial := NewMemory()
	require.NoError(t, partial.CreateSession(paramsForEpoch("chunky", 1)))
	require.NoError(t, partial.AppendDiffs("chunky", []types.DiffRecord{
		makeDiffRecord(1), makeDiffRecord(2), makeDiffRecord(3),
	}))
	n, err = MigrateSQLiteSessions(src, partial)
	require.NoError(t, err)
	require.Equal(t, 1, n)
	diffs, err = partial.GetDiffs("chunky", 1, 5)
	require.NoError(t, err)
	require.Len(t, diffs, 5)
}

func TestMigrateSQLiteSessions_SealedAndObs(t *testing.T) {
	srcDir := t.TempDir()
	src, err := NewSQLite(srcDir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = src.Close() })

	require.NoError(t, src.CreateSession(paramsForEpoch("obs", 2)))
	require.NoError(t, src.AppendDiff("obs", makeDiffRecord(1)))

	sealed := InferenceRow{
		InferenceID:        7,
		SealedNonce:        1,
		ObsPresent:         true,
		SealedStatus:       3,
		SealedExecutorSlot: 2,
		SealedModel:        "m",
		SealedPromptHash:   []byte("p"),
		SealedResponseHash: []byte("r"),
	}
	require.NoError(t, src.InsertSealedInference("obs", sealed))
	require.NoError(t, src.RecordValidationsAppliedOnce("obs", []ValidationObsEntry{
		{InferenceID: 7, SlotID: 2},
		{InferenceID: 7, SlotID: 3},
	}))

	dest := NewMemory()
	n, err := MigrateSQLiteSessions(src, dest)
	require.NoError(t, err)
	require.Equal(t, 1, n)

	got, ok, err := dest.GetSealedInference("obs", 7)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, sealed.SealedNonce, got.SealedNonce)
	require.Equal(t, sealed.SealedStatus, got.SealedStatus)
	require.Equal(t, sealed.SealedModel, got.SealedModel)

	rows, err := dest.GetValidationObservability("obs")
	require.NoError(t, err)
	require.NotEmpty(t, rows)

	// Idempotent.
	n, err = MigrateSQLiteSessions(src, dest)
	require.NoError(t, err)
	require.Equal(t, 1, n)
	got2, ok, err := dest.GetSealedInference("obs", 7)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, got, got2)
}

func TestQuarantineSQLiteArtifacts(t *testing.T) {
	dir := t.TempDir()
	src, err := NewSQLite(dir)
	require.NoError(t, err)
	require.NoError(t, src.CreateSession(paramsForEpoch("q", 1)))
	require.NoError(t, src.Close())

	has, err := HasSQLiteSessions(dir)
	require.NoError(t, err)
	require.True(t, has)

	require.NoError(t, quarantineSQLiteArtifacts(dir))

	has, err = HasSQLiteSessions(dir)
	require.NoError(t, err)
	require.False(t, has)
	arts, err := HasSQLiteArtifacts(dir)
	require.NoError(t, err)
	require.False(t, arts)
}
