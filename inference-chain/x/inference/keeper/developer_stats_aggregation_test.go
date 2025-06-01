package keeper_test

import (
	"github.com/google/uuid"
	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/assert"
	"testing"
	"time"
)

func TestDeveloperStats(t *testing.T) {
	const (
		developer1 = "developer1"
		developer2 = "developer2"

		epochId1                   = uint64(1)
		epochId2                   = 2
		developer1Inference1Tokens = uint64(10)
	)

	developer1Inference1 := uuid.New()
	developer1Inference2 := uuid.New()

	developer2Inference1 := uuid.New()
	t.Parallel()

	t.Run("get stats by one developer and one epoch", func(t *testing.T) {
		keeper, ctx := keepertest.InferenceKeeper(t)

		assert.NoError(t, keeper.DevelopersStatsSet(ctx, developer1, developer1Inference1.String(), types.InferenceStatus_STARTED, epochId1, developer1Inference1Tokens))
		time.Sleep(3 * time.Second)

		assert.NoError(t, keeper.DevelopersStatsSet(ctx, developer1, developer1Inference2.String(), types.InferenceStatus_VALIDATED, epochId1, developer1Inference1Tokens))
		time.Sleep(1 * time.Second)

		statsByEpoch, ok := keeper.DevelopersStatsGetByEpoch(ctx, developer1, epochId1)
		assert.True(t, ok)
		assert.Equal(t, epochId1, statsByEpoch.EpochId)
		assert.Equal(t, 2, len(statsByEpoch.Inferences))

		inferenceStat1 := statsByEpoch.Inferences[developer1Inference1.String()]
		assert.Equal(t, types.InferenceStatus_STARTED, inferenceStat1.Status)
		assert.Equal(t, developer1Inference1Tokens, inferenceStat1.AiTokensUsed)

		inferenceStat2 := statsByEpoch.Inferences[developer1Inference2.String()]
		assert.Equal(t, types.InferenceStatus_VALIDATED, inferenceStat2.Status)
		assert.Equal(t, developer1Inference1Tokens, inferenceStat2.AiTokensUsed)

		now := time.Now().UTC()
		statsByTime := keeper.DevelopersStatsGetByTime(ctx, developer1, now.Add(-time.Second).Unix(), now.Unix())
		assert.Equal(t, 1, len(statsByTime))
		assert.Equal(t, types.InferenceStatus_VALIDATED, statsByTime[0].Inference.Status)
		assert.Equal(t, developer1Inference1Tokens, statsByTime[0].Inference.AiTokensUsed)
	})

	t.Run("get stats by 2 developers and one epoch", func(t *testing.T) {
		keeper, ctx := keepertest.InferenceKeeper(t)

		assert.NoError(t, keeper.DevelopersStatsSet(ctx, developer1, developer1Inference1.String(), types.InferenceStatus_STARTED, epochId1, developer1Inference1Tokens))
		assert.NoError(t, keeper.DevelopersStatsSet(ctx, developer2, developer2Inference1.String(), types.InferenceStatus_FINISHED, epochId1, developer1Inference1Tokens+2))

		statsByEpoch, ok := keeper.DevelopersStatsGetByEpoch(ctx, developer1, epochId1)
		assert.True(t, ok)
		assert.Equal(t, epochId1, statsByEpoch.EpochId)
		assert.Equal(t, 1, len(statsByEpoch.Inferences))

		inferenceStat1 := statsByEpoch.Inferences[developer1Inference1.String()]
		assert.Equal(t, types.InferenceStatus_STARTED, inferenceStat1.Status)
		assert.Equal(t, developer1Inference1Tokens, inferenceStat1.AiTokensUsed)

		statsByEpoch2, ok := keeper.DevelopersStatsGetByEpoch(ctx, developer2, epochId1)
		assert.True(t, ok)
		assert.Equal(t, epochId1, statsByEpoch2.EpochId)
		assert.Equal(t, 1, len(statsByEpoch2.Inferences))

		inferenceStat2 := statsByEpoch2.Inferences[developer2Inference1.String()]
		assert.Equal(t, types.InferenceStatus_FINISHED, inferenceStat2.Status)
		assert.Equal(t, developer1Inference1Tokens+2, inferenceStat2.AiTokensUsed)
	})

	t.Run("update inference status", func(t *testing.T) {
		keeper, ctx := keepertest.InferenceKeeper(t)

		assert.NoError(t, keeper.DevelopersStatsSet(ctx, developer1, developer1Inference1.String(), types.InferenceStatus_STARTED, epochId1, developer1Inference1Tokens))
		assert.NoError(t, keeper.DevelopersStatsSet(ctx, developer1, developer1Inference1.String(), types.InferenceStatus_FINISHED, epochId1, developer1Inference1Tokens+3))

		statsByEpoch, ok := keeper.DevelopersStatsGetByEpoch(ctx, developer1, epochId1)
		assert.True(t, ok)
		assert.Equal(t, epochId1, statsByEpoch.EpochId)
		assert.Equal(t, 1, len(statsByEpoch.Inferences))

		inferenceStat := statsByEpoch.Inferences[developer1Inference1.String()]
		assert.Equal(t, types.InferenceStatus_FINISHED, inferenceStat.Status)
		assert.Equal(t, developer1Inference1Tokens+3, inferenceStat.AiTokensUsed)

		now := time.Now().UTC()
		statsByTime := keeper.DevelopersStatsGetByTime(ctx, developer1, now.Add(-time.Second).Unix(), now.Unix())
		assert.Equal(t, 1, len(statsByTime))
		assert.Equal(t, types.InferenceStatus_FINISHED, statsByTime[0].Inference.Status)
		assert.Equal(t, developer1Inference1Tokens+3, statsByTime[0].Inference.AiTokensUsed)

		assert.Error(t, keeper.DevelopersStatsSet(ctx, developer1, developer1Inference1.String(), types.InferenceStatus_VALIDATED, epochId2, developer1Inference1Tokens+3))
	})

	t.Run("inferences by time not found", func(t *testing.T) {
		keeper, ctx := keepertest.InferenceKeeper(t)

		assert.NoError(t, keeper.DevelopersStatsSet(ctx, developer1, developer1Inference1.String(), types.InferenceStatus_STARTED, epochId1, developer1Inference1Tokens))

		now := time.Now().UTC()
		statsByTime := keeper.DevelopersStatsGetByTime(ctx, developer1, now.Add(-time.Minute*2).Unix(), now.Add(-time.Minute).Unix())
		assert.Empty(t, statsByTime)
	})

	t.Run("count ai tokens and inference requests by time", func(t *testing.T) {
		keeper, ctx := keepertest.InferenceKeeper(t)

		assert.NoError(t, keeper.DevelopersStatsSet(ctx, developer1, developer1Inference1.String(), types.InferenceStatus_STARTED, epochId1, developer1Inference1Tokens*2))
		time.Sleep(3 * time.Second)

		assert.NoError(t, keeper.DevelopersStatsSet(ctx, developer2, developer1Inference2.String(), types.InferenceStatus_VALIDATED, epochId1, developer1Inference1Tokens))
		time.Sleep(2 * time.Second)

		assert.NoError(t, keeper.DevelopersStatsSet(ctx, developer1, uuid.New().String(), types.InferenceStatus_VALIDATED, epochId2, developer1Inference1Tokens))
		time.Sleep(1 * time.Second)

		now := time.Now().UTC()
		tokens, requests := keeper.CountTotalInferenceInPeriod(ctx, now.Add(-time.Second*10).Unix(), now.Add(-time.Second*2).Unix())
		assert.Equal(t, int64(developer1Inference1Tokens*3), tokens)
		assert.Equal(t, 2, requests)
	})

	t.Run("count ai tokens and inference by epochs and developer", func(t *testing.T) {
		const currentEpochId = uint64(3)

		keeper, ctx := keepertest.InferenceKeeper(t)

		assert.NoError(t, keeper.DevelopersStatsSet(ctx, developer1, developer1Inference1.String(), types.InferenceStatus_STARTED, epochId1, developer1Inference1Tokens*2))
		assert.NoError(t, keeper.DevelopersStatsSet(ctx, developer1, developer1Inference2.String(), types.InferenceStatus_VALIDATED, epochId2, developer1Inference1Tokens*2))
		assert.NoError(t, keeper.DevelopersStatsSet(ctx, developer2, uuid.New().String(), types.InferenceStatus_VALIDATED, epochId2, developer1Inference1Tokens))
		assert.NoError(t, keeper.DevelopersStatsSet(ctx, developer2, uuid.New().String(), types.InferenceStatus_VALIDATED, currentEpochId, developer1Inference1Tokens))

		tokens, requests := keeper.CountTotalInferenceInLastNEpochs(ctx, currentEpochId, 2)
		assert.Equal(t, int64(developer1Inference1Tokens*5), tokens)
		assert.Equal(t, 3, requests)

		tokens, requests = keeper.CountTotalInferenceInLastNEpochsByDeveloper(ctx, developer2, currentEpochId, 2)
		assert.Equal(t, int64(developer1Inference1Tokens), tokens)
		assert.Equal(t, 1, requests)
	})
}
