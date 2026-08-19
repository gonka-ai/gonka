package v0_2_16

import (
	"testing"

	"cosmossdk.io/collections"
	keepertest "github.com/productscience/inference/testutil/keeper"
	inferencetypes "github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func TestUpgradeName(t *testing.T) {
	require.Equal(t, "v0.2.16", UpgradeName)
}

func TestBackfillTrainingParamDefaults(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)

	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.TrainingParams = nil
	require.NoError(t, k.SetParams(ctx, params))

	require.NoError(t, backfillTrainingParamDefaults(ctx, k))

	got, err := k.GetParams(ctx)
	require.NoError(t, err)
	require.NotNil(t, got.TrainingParams)
	require.Equal(t, inferencetypes.DefaultTrainingOptInTtlBlocks, got.TrainingParams.OptInTtlBlocks)
	require.Equal(t, inferencetypes.DefaultTrainingReleaseBufferBlocks, got.TrainingParams.ReleaseBufferBlocks)
}

func TestBackfillTrainingParamDefaults_RaisesLimitsToEpochLength(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)

	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.EpochParams.EpochLength = 2000
	params.TrainingParams = nil
	require.NoError(t, k.SetParams(ctx, params))

	require.NoError(t, backfillTrainingParamDefaults(ctx, k))

	got, err := k.GetParams(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(2000), got.TrainingParams.OptInTtlBlocks)
	require.Equal(t, 2*int64(2000)+got.TrainingParams.ReleaseBufferBlocks, got.TrainingParams.SettledShardRetentionBlocks)

	got.TrainingParams.TrainingEnabled = true
	require.NoError(t, got.Validate())
}

func TestBackfillTrainingParamDefaults_PreservesOverrides(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)

	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.TrainingParams.OptInTtlBlocks = 777
	params.TrainingParams.ReleaseBufferBlocks = 0
	require.NoError(t, k.SetParams(ctx, params))

	require.NoError(t, backfillTrainingParamDefaults(ctx, k))

	got, err := k.GetParams(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(777), got.TrainingParams.OptInTtlBlocks)
	require.Equal(t, inferencetypes.DefaultTrainingReleaseBufferBlocks, got.TrainingParams.ReleaseBufferBlocks)
}

func TestBackfillTrainshardNodeStatuses(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)

	require.NoError(t, k.Trainshards.Set(ctx, 1, inferencetypes.Trainshard{
		TrainshardId: 1,
		Status:       inferencetypes.TrainshardStatus_TRAINSHARD_STATUS_ACTIVE,
		Nodes:        []*inferencetypes.TrainshardReservedNode{{Participant: "host", NodeId: "node-a"}},
	}))
	require.NoError(t, k.Trainshards.Set(ctx, 2, inferencetypes.Trainshard{
		TrainshardId:   2,
		Status:         inferencetypes.TrainshardStatus_TRAINSHARD_STATUS_SETTLED,
		ClosedAtHeight: 150,
		Nodes:          []*inferencetypes.TrainshardReservedNode{{Participant: "host", NodeId: "node-b"}},
	}))

	require.NoError(t, backfillTrainshardNodeStatuses(ctx, k))

	active, err := k.Trainshards.Get(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, inferencetypes.TrainshardNodeStatus_TRAINSHARD_NODE_STATUS_ACTIVE, active.Nodes[0].Status)

	closed, err := k.Trainshards.Get(ctx, 2)
	require.NoError(t, err)
	require.Equal(t, inferencetypes.TrainshardNodeStatus_TRAINSHARD_NODE_STATUS_RELEASED_ON_CLOSE, closed.Nodes[0].Status)
	require.Equal(t, int64(150), closed.Nodes[0].ReleasedAtHeight)
	require.Equal(t, int64(150), closed.Nodes[0].ReservedUntilHeight)
}

func TestClearStaleTrainingOptIns(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)

	require.NoError(t, k.TrainingNodeOptIns.Set(ctx, collections.Join("host", "node-a"), int64(0)))
	require.NoError(t, k.TrainingNodeOptIns.Set(ctx, collections.Join("host", "node-b"), int64(500)))

	require.NoError(t, clearStaleTrainingOptIns(ctx, k))

	has, err := k.TrainingNodeOptIns.Has(ctx, collections.Join("host", "node-a"))
	require.NoError(t, err)
	require.False(t, has)

	expiresAt, err := k.TrainingNodeOptIns.Get(ctx, collections.Join("host", "node-b"))
	require.NoError(t, err)
	require.Equal(t, int64(500), expiresAt)
}
