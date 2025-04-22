package training_test

import (
	"context"
	"testing"
	"time"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/training"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func TestRunManager_Join_And_RankAssignment(t *testing.T) {
	store := training.NewMockRunStore()
	runId := uint64(1)
	minNodes := 3
	maxNodes := 5

	rm := training.NewRunManager(runId, store, minNodes, maxNodes)

	// 1. Populate with a dummy training task
	initialTask := &types.TrainingTask{
		Id: runId,
		Epoch: &types.EpochInfo{
			LastEpoch:            -1, // Start before epoch 0
			LastEpochIsFinished:  false,
			LastEpochBlockHeight: 0,
			LastEpochTimestamp:   0,
		},
	}
	store.SetTrainingTask(initialTask)

	// For testing, we often don't need a fully functional context.
	// Using a zero-value sdk.Context might suffice if the tested code
	// doesn't rely heavily on context values (like BlockHeight/Time directly).
	// If it did, we'd need a more sophisticated mock context setup.
	baseCtx := sdk.Context{}
	blockHeight := int64(10)
	blockTime := time.Now()

	// Helper to create BlockInfo using the new function
	createBlockInfo := func(height int64, t time.Time) training.BlockInfo {
		return training.NewBlockInfoFromValues(height, t)
	}

	block1 := createBlockInfo(blockHeight, blockTime)

	// --- Participant 1 joins ---
	participant1 := "participantA"
	node1 := "node1"
	epoch0 := int32(0)

	// Pass sdk.Context
	err := rm.Join(baseCtx, node1, epoch0, block1, participant1)
	require.NoError(t, err)

	// Check RunState using standard context for store access
	storeCtx := context.Background()
	runState1, err := store.GetRunState(storeCtx, runId)
	require.NoError(t, err)
	require.NotNil(t, runState1)
	require.Equal(t, epoch0, runState1.Epoch.LastEpoch)
	require.False(t, runState1.Epoch.LastEpochIsFinished) // Not finished yet
	// Use getter
	require.Equal(t, block1.Height(), runState1.Epoch.LastEpochBlockHeight)

	// Check EpochState
	epochState1, err := store.GetEpochState(storeCtx, runId, epoch0)
	require.NoError(t, err)
	require.Len(t, epochState1, 1)
	require.Equal(t, participant1, epochState1[0].Participant)
	require.Equal(t, node1, epochState1[0].NodeId)
	require.Equal(t, int32(-1), epochState1[0].Rank) // Rank not assigned yet
	// Use getter
	require.Equal(t, block1.Height(), epochState1[0].BlockHeight)

	// --- Participant 2 joins ---
	blockHeight += 1
	blockTime = blockTime.Add(5 * time.Second)
	block2 := createBlockInfo(blockHeight, blockTime)
	participant2 := "participantB"
	node2 := "node2"

	// Pass sdk.Context
	err = rm.Join(baseCtx, node2, epoch0, block2, participant2)
	require.NoError(t, err)

	// Check RunState (should still be epoch 0, not finished)
	runState2, err := store.GetRunState(storeCtx, runId)
	require.NoError(t, err)
	require.Equal(t, epoch0, runState2.Epoch.LastEpoch)
	require.False(t, runState2.Epoch.LastEpochIsFinished)

	// Check EpochState
	epochState2, err := store.GetEpochState(storeCtx, runId, epoch0)
	require.NoError(t, err)
	require.Len(t, epochState2, 2)
	// Verify ranks are still -1 (sorting is done by GetEpochState in mock)
	require.Equal(t, int32(-1), epochState2[0].Rank)
	require.Equal(t, int32(-1), epochState2[1].Rank)

	// --- Participant 3 joins (minNodes reached) ---
	blockHeight += 1
	blockTime = blockTime.Add(5 * time.Second)
	block3 := createBlockInfo(blockHeight, blockTime)
	participant3 := "participantA" // Same participant, different node
	node3 := "node3"

	// Pass sdk.Context
	err = rm.Join(baseCtx, node3, epoch0, block3, participant3)
	require.NoError(t, err)

	// 4. Check ranks got assigned because minNodes (3) was reached

	// Check RunState (should now be finished)
	runState3, err := store.GetRunState(storeCtx, runId)
	require.NoError(t, err)
	require.Equal(t, epoch0, runState3.Epoch.LastEpoch)
	require.True(t, runState3.Epoch.LastEpochIsFinished) // Should be finished now

	// Check EpochState (ranks should be assigned)
	epochState3, err := store.GetEpochState(storeCtx, runId, epoch0)
	require.NoError(t, err)
	require.Len(t, epochState3, 3)

	// Check ranks are assigned (0, 1, 2). The mock store sorts activity.
	ranks := make(map[int32]bool)
	participantsFound := make(map[string]map[string]bool)
	for _, activity := range epochState3 {
		require.NotEqual(t, int32(-1), activity.Rank, "Rank should be assigned")
		ranks[activity.Rank] = true

		if _, ok := participantsFound[activity.Participant]; !ok {
			participantsFound[activity.Participant] = make(map[string]bool)
		}
		participantsFound[activity.Participant][activity.NodeId] = true
	}

	require.Len(t, ranks, minNodes, "Should have assigned ranks 0 to minNodes-1")
	require.True(t, ranks[0])
	require.True(t, ranks[1])
	require.True(t, ranks[2])

	// Verify the correct participants/nodes were included
	require.True(t, participantsFound[participant1][node1])
	require.True(t, participantsFound[participant2][node2])
	require.True(t, participantsFound[participant3][node3])

}

// Note: Removed commented out helper function as it was added to training_sync.go
