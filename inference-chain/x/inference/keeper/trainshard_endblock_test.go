package keeper_test

import (
	"testing"

	"cosmossdk.io/collections"
	"github.com/stretchr/testify/require"

	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/types"
)

// TestExpireTrainshards_StaleKeyDoesNotStarveBacklog cleans orphaned keys
func TestExpireTrainshards_StaleKeyDoesNotStarveBacklog(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)

	params := types.DefaultParams()
	params.TrainingParams.MaxExpirationsPerBlock = 1
	require.NoError(t, k.SetParams(ctx, params))

	// stale key sorts first but has no backing shard
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

	// block 1: the quota slot lands on the stale key
	k.ProcessTrainshardEndBlock(ctx)
	hasStale, err := k.TrainshardExpiryIndex.Has(ctx, collections.Join(int64(3), uint64(99)))
	require.NoError(t, err)
	require.False(t, hasStale)

	// block 2: the real shard reaches the front and expires
	k.ProcessTrainshardEndBlock(ctx)
	got, err := k.Trainshards.Get(ctx, shard.TrainshardId)
	require.NoError(t, err)
	require.Equal(t, types.TrainshardStatus_TRAINSHARD_STATUS_EXPIRED, got.Status)

	hasActive, err := k.TrainshardActiveIndex.Has(ctx, shard.TrainshardId)
	require.NoError(t, err)
	require.False(t, hasActive)
}

// TestExpireTrainshards_DrainsBacklogAcrossBlocks drains backlog across blocks
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
