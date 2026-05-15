package statsstorage

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func testFileStorage(t *testing.T) *FileStorage {
	t.Helper()
	dir := t.TempDir()
	return NewFileStorage(dir)
}

func TestFileUpsertDevshardEscrow_SingleEscrow(t *testing.T) {
	fs := testFileStorage(t)
	ctx := context.Background()

	escrow := DevshardEscrow{
		EscrowID:                  "esc-1",
		EpochID:                   10,
		Participant:               "gonka1dev",
		TotalCostInCoins:          5000,
		TotalInferences:           25,
		TotalPromptTokenCount:     1000,
		TotalCompletionTokenCount: 2000,
		TotalTokenCount:           3000,
		SettlementTimestamp:       1700000000000,
	}
	models := []DevshardModelAggregate{
		{Model: "gpt-4", PromptTokenCount: 600, CompletionTokenCount: 1200, TotalTokenCount: 1800, Inferences: 15, CostInCoins: 3000},
		{Model: "llama-3", PromptTokenCount: 400, CompletionTokenCount: 800, TotalTokenCount: 1200, Inferences: 10, CostInCoins: 2000},
	}

	err := fs.UpsertDevshardEscrow(ctx, escrow, models)
	require.NoError(t, err)

	// Verify file was created in devshard subdirectory.
	devDir := filepath.Join(fs.baseDir, "devshard")
	entries, err := os.ReadDir(devDir)
	require.NoError(t, err)
	require.Len(t, entries, 1)

	// Verify data can be read back.
	records, err := fs.readAllDevshardRecords()
	require.NoError(t, err)
	require.Len(t, records, 1)
	require.Equal(t, "esc-1", records[0].Escrow.EscrowID)
	require.Equal(t, uint64(10), records[0].Escrow.EpochID)
	require.Equal(t, "gonka1dev", records[0].Escrow.Participant)
	require.Equal(t, int32(25), records[0].Escrow.TotalInferences)
	require.Equal(t, uint64(3000), records[0].Escrow.TotalTokenCount)
	require.Equal(t, int64(5000), records[0].Escrow.TotalCostInCoins)
	require.Equal(t, UnixMillis(1700000000000), records[0].Escrow.SettlementTimestamp)
	require.Len(t, records[0].ModelStats, 2)
}

func TestFileUpsertDevshardEscrow_Idempotent(t *testing.T) {
	fs := testFileStorage(t)
	ctx := context.Background()

	escrow := DevshardEscrow{
		EscrowID:            "esc-idem",
		EpochID:             5,
		Participant:         "gonka1a",
		TotalCostInCoins:    100,
		TotalInferences:     2,
		TotalTokenCount:     30,
		SettlementTimestamp: 1700000001000,
	}
	models := []DevshardModelAggregate{
		{Model: "model-x", TotalTokenCount: 30, Inferences: 2, CostInCoins: 100},
	}

	// First upsert.
	require.NoError(t, fs.UpsertDevshardEscrow(ctx, escrow, models))

	// Second upsert with updated cost — should overwrite, not duplicate.
	escrow.TotalCostInCoins = 200
	require.NoError(t, fs.UpsertDevshardEscrow(ctx, escrow, models))

	records, err := fs.readAllDevshardRecords()
	require.NoError(t, err)
	require.Len(t, records, 1, "re-upsert should overwrite, not create duplicate")
	require.Equal(t, int64(200), records[0].Escrow.TotalCostInCoins)
}

func TestFileUpsertDevshardEscrow_ModelSetReplacement(t *testing.T) {
	fs := testFileStorage(t)
	ctx := context.Background()

	escrow := DevshardEscrow{
		EscrowID:            "esc-replace",
		EpochID:             8,
		Participant:         "gonka1c",
		TotalCostInCoins:    500,
		TotalInferences:     10,
		TotalTokenCount:     300,
		SettlementTimestamp: 1700000003000,
	}

	// First upsert: two models.
	twoModels := []DevshardModelAggregate{
		{Model: "gpt-4", TotalTokenCount: 180, Inferences: 6, CostInCoins: 300},
		{Model: "llama-3", TotalTokenCount: 120, Inferences: 4, CostInCoins: 200},
	}
	require.NoError(t, fs.UpsertDevshardEscrow(ctx, escrow, twoModels))

	records, err := fs.readAllDevshardRecords()
	require.NoError(t, err)
	require.Len(t, records[0].ModelStats, 2, "first upsert should have 2 models")

	// Second upsert: only one model — file overwrite naturally drops the stale model.
	oneModel := []DevshardModelAggregate{
		{Model: "gpt-4", TotalTokenCount: 300, Inferences: 10, CostInCoins: 500},
	}
	require.NoError(t, fs.UpsertDevshardEscrow(ctx, escrow, oneModel))

	records, err = fs.readAllDevshardRecords()
	require.NoError(t, err)
	require.Len(t, records, 1)
	require.Len(t, records[0].ModelStats, 1, "second upsert must replace model set entirely")
	require.Equal(t, "gpt-4", records[0].ModelStats[0].Model)
}

func TestFileUpsertDevshardEscrow_NoModels(t *testing.T) {
	fs := testFileStorage(t)
	ctx := context.Background()

	escrow := DevshardEscrow{
		EscrowID:            "esc-nomodel",
		EpochID:             7,
		Participant:         "gonka1b",
		SettlementTimestamp: 1700000002000,
	}

	err := fs.UpsertDevshardEscrow(ctx, escrow, nil)
	require.NoError(t, err)

	records, err := fs.readAllDevshardRecords()
	require.NoError(t, err)
	require.Len(t, records, 1)
	require.Empty(t, records[0].ModelStats)
}

func TestFileUpsertDevshardEscrow_MissingEscrowID(t *testing.T) {
	fs := testFileStorage(t)
	ctx := context.Background()

	err := fs.UpsertDevshardEscrow(ctx, DevshardEscrow{}, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "escrow_id is required")
}

func TestFileUpsertDevshardEscrow_MultipleEscrows(t *testing.T) {
	fs := testFileStorage(t)
	ctx := context.Background()

	escrowA := DevshardEscrow{EscrowID: "esc-a", EpochID: 1, Participant: "dev1", SettlementTimestamp: 1000}
	escrowB := DevshardEscrow{EscrowID: "esc-b", EpochID: 2, Participant: "dev2", SettlementTimestamp: 2000}
	escrowASameEpoch := DevshardEscrow{EscrowID: "esc-a", EpochID: 2, Participant: "dev1", SettlementTimestamp: 3000}

	require.NoError(t, fs.UpsertDevshardEscrow(ctx, escrowA, nil))
	require.NoError(t, fs.UpsertDevshardEscrow(ctx, escrowB, nil))
	require.NoError(t, fs.UpsertDevshardEscrow(ctx, escrowASameEpoch, nil))

	records, err := fs.readAllDevshardRecords()
	require.NoError(t, err)
	require.Len(t, records, 3, "different (escrow_id, epoch_id) combos should produce separate files")
}

func TestFilePruneOlderThan_IncludesDevshardFiles(t *testing.T) {
	fs := testFileStorage(t)
	ctx := context.Background()

	oldEscrow := DevshardEscrow{
		EscrowID:            "esc-old",
		EpochID:             1,
		Participant:         "gonka1old",
		TotalCostInCoins:    100,
		TotalInferences:     1,
		TotalTokenCount:     10,
		SettlementTimestamp: 1000, // very old
	}
	recentEscrow := DevshardEscrow{
		EscrowID:            "esc-new",
		EpochID:             2,
		Participant:         "gonka1new",
		TotalCostInCoins:    200,
		TotalInferences:     2,
		TotalTokenCount:     20,
		SettlementTimestamp: 9999999999999,
	}
	oldModels := []DevshardModelAggregate{{Model: "old-model", TotalTokenCount: 10, Inferences: 1}}
	newModels := []DevshardModelAggregate{{Model: "new-model", TotalTokenCount: 20, Inferences: 2}}

	require.NoError(t, fs.UpsertDevshardEscrow(ctx, oldEscrow, oldModels))
	require.NoError(t, fs.UpsertDevshardEscrow(ctx, recentEscrow, newModels))

	// Prune with cutoff that removes the old escrow.
	err := fs.PruneOlderThan(ctx, 5000)
	require.NoError(t, err)

	records, err := fs.readAllDevshardRecords()
	require.NoError(t, err)
	require.Len(t, records, 1, "old devshard file should be pruned")
	require.Equal(t, "esc-new", records[0].Escrow.EscrowID)
}

func TestFileUpsertDevshardEscrow_DoesNotAffectInferenceRecords(t *testing.T) {
	fs := testFileStorage(t)
	ctx := context.Background()

	// Write an inference record.
	infRec := InferenceRecord{
		InferenceID:          "inf-isolation",
		RequestedBy:          "gonka1dev",
		Model:                "gpt-4",
		Status:               "FINISHED",
		EpochID:              10,
		PromptTokenCount:     100,
		CompletionTokenCount: 50,
		TotalTokenCount:      150,
		ActualCostInCoins:    999,
		StartBlockTimestamp:  1700000000000,
		EndBlockTimestamp:    1700000001000,
	}
	require.NoError(t, fs.UpsertInference(ctx, infRec))

	// Write a devshard escrow.
	escrow := DevshardEscrow{
		EscrowID:            "esc-iso",
		EpochID:             10,
		Participant:         "gonka1dev",
		TotalCostInCoins:    5000,
		TotalInferences:     25,
		TotalTokenCount:     3000,
		SettlementTimestamp: 1700000002000,
	}
	require.NoError(t, fs.UpsertDevshardEscrow(ctx, escrow, nil))

	// Verify inference records are untouched (devshard dir is separate).
	recs, err := fs.GetDeveloperInferencesByTime(ctx, "gonka1dev", 0, 9999999999999)
	require.NoError(t, err)
	require.Len(t, recs, 1)
	require.Equal(t, "inf-isolation", recs[0].InferenceID)
	require.Equal(t, int64(999), recs[0].ActualCostInCoins)

	// Verify devshard records are also present.
	devRecords, err := fs.readAllDevshardRecords()
	require.NoError(t, err)
	require.Len(t, devRecords, 1)
	require.Equal(t, "esc-iso", devRecords[0].Escrow.EscrowID)
}
