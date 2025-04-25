package keeper_test

import (
	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestTrainNodeActivity(t *testing.T) {
	keeper, ctx := keepertest.InferenceKeeper(t)

	taskId := uint64(1)
	epoch := int32(1)
	participant := "participant1"
	nodeId := "node1"
	keeper.SetTrainingTaskNodeEpochActivity(ctx, &types.TrainingTaskNodeEpochActivity{
		TaskId:      taskId,
		Participant: participant,
		NodeId:      nodeId,
		Epoch:       epoch,
		BlockTime:   111,
		BlockHeight: 20,
		Rank:        10,
	})

	activity, err := keeper.GetTrainingTaskNodeActivityAtEpoch(ctx, taskId, epoch)
	require.NoError(t, err)
	require.Len(t, activity, 1)

	_, found := keeper.GetTrainingTaskNodeEpochActivity(ctx, taskId, epoch, participant, nodeId)
	require.True(t, found)
}
