package keeper_test

import (
	"testing"

	"cosmossdk.io/collections"
	"github.com/stretchr/testify/require"

	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/types"
)

func TestExpireTrainshards_StaleKeyDoesNotStarveBacklog(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)

	params := types.DefaultParams()
	params.TrainingParams.MaxExpirationsPerBlock = 1
	require.NoError(t, k.SetParams(ctx, params))

	require.NoError(t, k.TrainshardExpiryIndex.Set(ctx, collections.Join(int64(3), uint64(99))))

	shard := types.Trainshard{
		TrainshardId:    1,
		Creator:         "creator",
		ExpiresAtHeight: 5,
		Status:          types.TrainshardStatus_TRAINSHARD_STATUS_ACTIVE,
	}
	require.NoError(t, k.Trainshards.Set(ctx, shard.TrainshardId, shard))
	require.NoError(t, k.TrainshardActiveIndex.Set(ctx, shard.TrainshardId))
	require.NoError(t, k.TrainshardExpiryIndex.Set(ctx, collections.Join(shard.ExpiresAtHeight, shard.TrainshardId)))

	ctx = ctx.WithBlockHeight(20)

	k.ProcessTrainshardEndBlock(ctx)
	hasStale, err := k.TrainshardExpiryIndex.Has(ctx, collections.Join(int64(3), uint64(99)))
	require.NoError(t, err)
	require.False(t, hasStale)

	k.ProcessTrainshardEndBlock(ctx)
	got, err := k.Trainshards.Get(ctx, shard.TrainshardId)
	require.NoError(t, err)
	require.Equal(t, types.TrainshardStatus_TRAINSHARD_STATUS_EXPIRED, got.Status)

	hasActive, err := k.TrainshardActiveIndex.Has(ctx, shard.TrainshardId)
	require.NoError(t, err)
	require.False(t, hasActive)
}

func TestExpireTrainshards_DeferredKeyStillCloses(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	require.NoError(t, k.SetParams(ctx, types.DefaultParams()))

	shard := types.Trainshard{
		TrainshardId:    1,
		Creator:         "creator",
		ExpiresAtHeight: 5,
		Status:          types.TrainshardStatus_TRAINSHARD_STATUS_ACTIVE,
	}
	require.NoError(t, k.Trainshards.Set(ctx, shard.TrainshardId, shard))
	require.NoError(t, k.TrainshardActiveIndex.Set(ctx, shard.TrainshardId))
	require.NoError(t, k.TrainshardExpiryIndex.Set(ctx, collections.Join(int64(7), shard.TrainshardId)))

	k.ProcessTrainshardEndBlock(ctx.WithBlockHeight(20))

	got, err := k.Trainshards.Get(ctx, shard.TrainshardId)
	require.NoError(t, err)
	require.Equal(t, types.TrainshardStatus_TRAINSHARD_STATUS_EXPIRED, got.Status)

	hasDeferred, err := k.TrainshardExpiryIndex.Has(ctx, collections.Join(int64(7), shard.TrainshardId))
	require.NoError(t, err)
	require.False(t, hasDeferred)

	hasPlanned, err := k.TrainshardExpiryIndex.Has(ctx, collections.Join(shard.ExpiresAtHeight, shard.TrainshardId))
	require.NoError(t, err)
	require.False(t, hasPlanned)
}

func TestPruneClosedTrainshards_KeepsTwoEpochsEvenIfRetentionWasSetTooLow(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)

	params := types.DefaultParams()
	params.TrainingParams.SettledShardRetentionBlocks = 1
	require.NoError(t, k.SetParams(ctx, params))

	const closedAt = int64(10)
	shard := types.Trainshard{
		TrainshardId:   1,
		Creator:        "creator",
		Status:         types.TrainshardStatus_TRAINSHARD_STATUS_SETTLED,
		ClosedAtHeight: closedAt,
	}
	require.NoError(t, k.Trainshards.Set(ctx, shard.TrainshardId, shard))
	require.NoError(t, k.TrainshardClosedIndex.Set(ctx, collections.Join(closedAt, shard.TrainshardId)))

	floor := 2*params.EpochParams.EpochLength + params.TrainingParams.ReleaseBufferBlocks

	k.ProcessTrainshardEndBlock(ctx.WithBlockHeight(closedAt + floor - 1))
	_, err := k.Trainshards.Get(ctx, shard.TrainshardId)
	require.NoError(t, err)

	k.ProcessTrainshardEndBlock(ctx.WithBlockHeight(closedAt + floor))
	_, err = k.Trainshards.Get(ctx, shard.TrainshardId)
	require.ErrorIs(t, err, collections.ErrNotFound)
}

func TestExpireTrainshards_DrainsBacklogAcrossBlocks(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)

	params := types.DefaultParams()
	params.TrainingParams.MaxExpirationsPerBlock = 2
	require.NoError(t, k.SetParams(ctx, params))

	const n = uint64(5)
	for id := uint64(1); id <= n; id++ {
		shard := types.Trainshard{
			TrainshardId:    id,
			Creator:         "creator",
			ExpiresAtHeight: 5,
			Status:          types.TrainshardStatus_TRAINSHARD_STATUS_ACTIVE,
		}
		require.NoError(t, k.Trainshards.Set(ctx, id, shard))
		require.NoError(t, k.TrainshardActiveIndex.Set(ctx, id))
		require.NoError(t, k.TrainshardExpiryIndex.Set(ctx, collections.Join(shard.ExpiresAtHeight, id)))
	}
	ctx = ctx.WithBlockHeight(20)

	countExpired := func() int {
		c := 0
		for id := uint64(1); id <= n; id++ {
			s, err := k.Trainshards.Get(ctx, id)
			require.NoError(t, err)
			if s.Status == types.TrainshardStatus_TRAINSHARD_STATUS_EXPIRED {
				c++
			}
		}
		return c
	}

	k.ProcessTrainshardEndBlock(ctx)
	require.Equal(t, 2, countExpired())
	k.ProcessTrainshardEndBlock(ctx)
	require.Equal(t, 4, countExpired())
	k.ProcessTrainshardEndBlock(ctx)
	require.Equal(t, 5, countExpired())
}
