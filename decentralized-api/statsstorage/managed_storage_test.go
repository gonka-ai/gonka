package statsstorage

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestManagedStoragePruneOnStartup_PrunesInferenceAndDevshard(t *testing.T) {
	base := NewFileStorage(t.TempDir())
	ctx := context.Background()
	now := time.Now()

	oldTs := UnixMillis(now.Add(-48 * time.Hour).UnixMilli())
	recentTs := UnixMillis(now.Add(-1 * time.Hour).UnixMilli())

	require.NoError(t, base.UpsertInference(ctx, InferenceRecord{
		InferenceID:        "inf-old",
		RequestedBy:        "gonka1dev",
		Model:              "model-a",
		Status:             "FINISHED",
		EpochID:            1,
		TotalTokenCount:    100,
		ActualCostInCoins:  10,
		InferenceTimestamp: oldTs,
	}))
	require.NoError(t, base.UpsertInference(ctx, InferenceRecord{
		InferenceID:        "inf-recent",
		RequestedBy:        "gonka1dev",
		Model:              "model-a",
		Status:             "FINISHED",
		EpochID:            2,
		TotalTokenCount:    200,
		ActualCostInCoins:  20,
		InferenceTimestamp: recentTs,
	}))

	require.NoError(t, base.UpsertDevshardEscrow(ctx, DevshardEscrow{
		EscrowID:            "esc-old",
		EpochID:             10,
		Participant:         "gonka1dev",
		TotalCostInCoins:    30,
		TotalInferences:     3,
		TotalTokenCount:     300,
		SettlementTimestamp: oldTs,
	}, nil))
	require.NoError(t, base.UpsertDevshardEscrow(ctx, DevshardEscrow{
		EscrowID:            "esc-recent",
		EpochID:             11,
		Participant:         "gonka1dev",
		TotalCostInCoins:    40,
		TotalInferences:     4,
		TotalTokenCount:     400,
		SettlementTimestamp: recentTs,
	}, nil))

	managed := NewManagedStorage(base, 1)
	t.Cleanup(func() { managed.Close() })

	require.Eventually(t, func() bool {
		infs, err := base.GetDeveloperInferencesByTime(ctx, "gonka1dev", 0, UnixMillis(now.Add(24*time.Hour).UnixMilli()))
		if err != nil {
			return false
		}
		devshards, err := base.readAllDevshardRecords()
		if err != nil {
			return false
		}
		if len(infs) != 1 || len(devshards) != 1 {
			return false
		}
		return infs[0].InferenceID == "inf-recent" && devshards[0].Escrow.EscrowID == "esc-recent"
	}, 2*time.Second, 20*time.Millisecond)
}
