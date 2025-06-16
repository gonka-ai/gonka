package training_test

import (
	keepertest "github.com/productscience/inference/testutil/keeper"
	keeper2 "github.com/productscience/inference/x/inference/keeper"
	"testing"
	"time"

	"github.com/productscience/inference/x/inference/training"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func TestRunManager_Join_And_RankAssignment(t *testing.T) {
	keeper, keeperCtx := keepertest.InferenceKeeper(t)
	store := keeper2.NewKeeperTrainingRunStore(keeper)
	runId := uint64(1)

	rm := training.NewRunManager(runId, store, keeper)

	// 1. Populate with a dummy training task
	initialTask := &types.TrainingTask{
		Id:    runId,
		Epoch: training.NewEmptyEpochInfo(),
		Assignees: []*types.TrainingTaskAssignee{
			{
				Participant: "participantA",
				NodeIds:     []string{"node1"},
			},
			{
				Participant: "participantB",
				NodeIds:     []string{"node2"},
			},
			{
				Participant: "participantC",
				NodeIds:     []string{"node3"},
			},
		},
	}
	keeper.SetTrainingTask(keeperCtx, initialTask)

	baseCtx := keeperCtx
	blockHeight := int64(10)
	blockTime := time.Now()

	// Helper to create BlockInfo using the new function
	createBlockInfo := func(height int64, t time.Time) training.BlockInfo {
		return training.NewBlockInfoFromValues(height, t)
	}

	block1 := createBlockInfo(blockHeight, blockTime)

	// --- Participant 1 joins ---
	participant1 := "participantA"
	node1 := training.GlobalNodeId{Participant: participant1, LocalNodeId: "node1"}
	startingEpoch := int32(-1)

	err := rm.Join(baseCtx, node1, startingEpoch, block1)
	require.NoError(t, err)

	// Check RunState using standard context for store access
	storeCtx := baseCtx
	runState1 := store.GetRunState(storeCtx, runId)
	require.NotNil(t, runState1)
	require.Equal(t, startingEpoch, runState1.Epoch.LastEpoch)
	require.False(t, runState1.Epoch.LastEpochIsFinished) // Not finished yet

	// Check EpochState
	epochState1, err := store.GetEpochState(storeCtx, runId, startingEpoch)
	require.NoError(t, err)
	require.Len(t, epochState1, 1)
	require.Equal(t, participant1, epochState1[0].Participant)
	require.Equal(t, node1, training.GlobalNodeId{Participant: epochState1[0].Participant, LocalNodeId: epochState1[0].NodeId})
	require.Equal(t, int32(-1), epochState1[0].Rank) // Rank not assigned yet
	require.Equal(t, block1.Height(), epochState1[0].Heartbeat.BlockHeight)

	// --- Participant 2 joins ---
	blockHeight += 1
	blockTime = blockTime.Add(5 * time.Second)
	block2 := createBlockInfo(blockHeight, blockTime)
	participant2 := "participantB"
	node2 := training.GlobalNodeId{participant2, "node2"}

	// Pass sdk.Context
	err = rm.Join(baseCtx, node2, startingEpoch, block2)
	require.NoError(t, err)

	// Check RunState (should still be epoch 0, not finished)
	runState2 := store.GetRunState(storeCtx, runId)
	require.NotNil(t, runState2)
	require.Equal(t, startingEpoch, runState2.Epoch.LastEpoch)
	require.False(t, runState2.Epoch.LastEpochIsFinished)

	// Check EpochState
	epochState2, err := store.GetEpochState(storeCtx, runId, startingEpoch)
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
	node3 := training.GlobalNodeId{Participant: participant3, LocalNodeId: "node3"}

	err = rm.Join(baseCtx, node3, startingEpoch, block3)
	require.NoError(t, err)

	// 4. Check ranks got assigned because minNodes (3) was reached

	// Check RunState (should now be finished)
	runState3 := store.GetRunState(storeCtx, runId)
	require.NotNil(t, runState3)
	require.Equal(t, startingEpoch, runState3.Epoch.LastEpoch)
	require.True(t, runState3.Epoch.LastEpochIsFinished) // Should be finished now

	// Check EpochState (ranks should be assigned)
	epochState3, err := store.GetEpochState(storeCtx, runId, startingEpoch)
	require.NoError(t, err)
	require.Len(t, epochState3, 3)

	// Check ranks are assigned (0, 1, 2). The mock store sorts activity.
	ranks := make(map[int32]bool)
	participantsFound := make(map[string]map[training.GlobalNodeId]bool)
	for _, activity := range epochState3 {
		require.NotEqual(t, int32(-1), activity.Rank, "Rank should be assigned")
		ranks[activity.Rank] = true

		if _, ok := participantsFound[activity.Participant]; !ok {
			participantsFound[activity.Participant] = make(map[training.GlobalNodeId]bool)
		}
		gid := training.GlobalNodeId{Participant: activity.Participant, LocalNodeId: activity.NodeId}
		participantsFound[activity.Participant][gid] = true
	}

	require.Len(t, ranks, len(runState1.Assignees))
	require.True(t, ranks[0])
	require.True(t, ranks[1])
	require.True(t, ranks[2])

	// Verify the correct participants/nodes were included
	require.True(t, participantsFound[participant1][node1])
	require.True(t, participantsFound[participant2][node2])
	require.True(t, participantsFound[participant3][node3])

}
