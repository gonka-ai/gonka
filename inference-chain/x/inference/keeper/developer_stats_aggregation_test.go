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

		epochId1 = uint64(1)
		epochId2 = uint64(2)
		tokens   = uint64(10)
	)

	inference1Developer1 := types.Inference{
		InferenceId:          uuid.New().String(),
		PromptTokenCount:     tokens,
		CompletionTokenCount: tokens * 2,
		RequestedBy:          developer1,
		Status:               types.InferenceStatus_STARTED,
		StartBlockTimestamp:  time.Now().Add(-time.Second * 3).UTC().Unix(),
	}

	inference2Developer1 := types.Inference{
		InferenceId:          uuid.New().String(),
		PromptTokenCount:     tokens,
		CompletionTokenCount: tokens,
		RequestedBy:          developer1,
		Status:               types.InferenceStatus_VALIDATED,
		StartBlockTimestamp:  time.Now().UTC().Unix(),
	}

	inference1Developer2 := types.Inference{
		InferenceId:          uuid.New().String(),
		PromptTokenCount:     tokens * 3,
		CompletionTokenCount: tokens,
		RequestedBy:          developer2,
		Status:               types.InferenceStatus_FINISHED,
		StartBlockTimestamp:  time.Now().UTC().Unix(),
		EpochGroupId:         epochId2,
	}

	inference2Developer2 := types.Inference{
		InferenceId:          uuid.New().String(),
		PromptTokenCount:     tokens,
		CompletionTokenCount: tokens,
		RequestedBy:          developer2,
		Status:               types.InferenceStatus_EXPIRED,
		StartBlockTimestamp:  time.Now().UTC().Unix(),
	}

	t.Parallel()

	t.Run("get stats by one developer and one epoch", func(t *testing.T) {
		keeper, ctx := keepertest.InferenceKeeper(t)
		keeper.SetEpochGroupData(ctx, types.EpochGroupData{EpochGroupId: epochId1})

		assert.NoError(t, keeper.DevelopersStatsSet(ctx, inference1Developer1))
		assert.NoError(t, keeper.DevelopersStatsSet(ctx, inference2Developer1))

		statsByEpoch, ok := keeper.DevelopersStatsGetByEpoch(ctx, developer1, epochId1)
		assert.True(t, ok)
		assert.Equal(t, epochId1, statsByEpoch.EpochId)
		assert.Equal(t, 2, len(statsByEpoch.Inferences))

		inferenceStat1 := statsByEpoch.Inferences[inference1Developer1.GetInferenceId()]
		assert.Equal(t, inference1Developer1.Status, inferenceStat1.Status)
		assert.Equal(t, inference1Developer1.CompletionTokenCount+inference1Developer1.PromptTokenCount, inferenceStat1.AiTokensUsed)

		inferenceStat2 := statsByEpoch.Inferences[inference2Developer1.GetInferenceId()]
		assert.Equal(t, inference2Developer1.Status, inferenceStat2.Status)
		assert.Equal(t, inference2Developer1.CompletionTokenCount+inference2Developer1.PromptTokenCount, inferenceStat2.AiTokensUsed)

		now := time.Now().UTC()
		statsByTime := keeper.DevelopersStatsGetByTime(ctx, developer1, now.Add(-time.Second).Unix(), now.Unix())
		assert.Equal(t, 1, len(statsByTime))
		assert.Equal(t, inference2Developer1.Status, statsByTime[0].Inference.Status)
		assert.Equal(t, inference2Developer1.CompletionTokenCount+inference2Developer1.PromptTokenCount, statsByTime[0].Inference.AiTokensUsed)
	})

	t.Run("get stats by 2 developers and one epoch", func(t *testing.T) {
		keeper, ctx := keepertest.InferenceKeeper(t)
		keeper.SetEpochGroupData(ctx, types.EpochGroupData{EpochGroupId: epochId1})

		assert.NoError(t, keeper.DevelopersStatsSet(ctx, inference1Developer1))
		assert.NoError(t, keeper.DevelopersStatsSet(ctx, inference2Developer2))

		statsByEpoch1, ok := keeper.DevelopersStatsGetByEpoch(ctx, developer1, epochId1)
		assert.True(t, ok)
		assert.Equal(t, epochId1, statsByEpoch1.EpochId)
		assert.Len(t, statsByEpoch1.Inferences, 1)

		stat1 := statsByEpoch1.Inferences[inference1Developer1.InferenceId]
		assert.Equal(t, inference1Developer1.Status, stat1.Status)
		assert.Equal(t, inference1Developer1.PromptTokenCount+inference1Developer1.CompletionTokenCount, stat1.AiTokensUsed)

		statsByEpoch2, ok := keeper.DevelopersStatsGetByEpoch(ctx, developer2, epochId1)
		assert.True(t, ok)
		assert.Equal(t, epochId1, statsByEpoch2.EpochId)
		assert.Len(t, statsByEpoch2.Inferences, 1)

		stat2 := statsByEpoch2.Inferences[inference2Developer2.InferenceId]
		assert.Equal(t, inference2Developer2.Status, stat2.Status)
		assert.Equal(t, inference2Developer2.PromptTokenCount+inference2Developer2.CompletionTokenCount, stat2.AiTokensUsed)
	})

	t.Run("update inference status and epoch", func(t *testing.T) {
		keeper, ctx := keepertest.InferenceKeeper(t)
		keeper.SetEpochGroupData(ctx, types.EpochGroupData{EpochGroupId: epochId1})

		updatedInference := inference1Developer1
		updatedInference.Status = types.InferenceStatus_FINISHED
		updatedInference.EpochGroupId = epochId2

		assert.NoError(t, keeper.DevelopersStatsSet(ctx, inference1Developer1))

		keeper.SetEpochGroupData(ctx, types.EpochGroupData{EpochGroupId: epochId2})
		assert.NoError(t, keeper.DevelopersStatsSet(ctx, updatedInference))

		statsByEpoch, ok := keeper.DevelopersStatsGetByEpoch(ctx, developer1, epochId1)
		assert.True(t, ok)
		assert.Equal(t, epochId1, statsByEpoch.EpochId)
		assert.Equal(t, 0, len(statsByEpoch.Inferences))

		statsByEpoch, ok = keeper.DevelopersStatsGetByEpoch(ctx, developer1, epochId2)
		assert.True(t, ok)
		assert.Equal(t, epochId2, statsByEpoch.EpochId)
		assert.Equal(t, 1, len(statsByEpoch.Inferences))

		inferenceStat := statsByEpoch.Inferences[updatedInference.InferenceId]
		assert.Equal(t, updatedInference.Status, inferenceStat.Status)
		assert.Equal(t, updatedInference.PromptTokenCount+updatedInference.CompletionTokenCount, inferenceStat.AiTokensUsed)

		now := time.Now().UTC()
		statsByTime := keeper.DevelopersStatsGetByTime(ctx, developer1, now.Add(-time.Second*4).Unix(), now.Unix())
		assert.Equal(t, 1, len(statsByTime))
		assert.Equal(t, updatedInference.Status, statsByTime[0].Inference.Status)
		assert.Equal(t, updatedInference.PromptTokenCount+updatedInference.CompletionTokenCount, statsByTime[0].Inference.AiTokensUsed)
	})

	t.Run("inferences by time not found", func(t *testing.T) {
		keeper, ctx := keepertest.InferenceKeeper(t)

		assert.NoError(t, keeper.DevelopersStatsSet(ctx, inference1Developer1))

		now := time.Now().UTC()
		statsByTime := keeper.DevelopersStatsGetByTime(ctx, developer1, now.Add(-time.Minute*2).Unix(), now.Add(-time.Minute).Unix())
		assert.Empty(t, statsByTime)
	})

	t.Run("count ai tokens and inference requests by time", func(t *testing.T) {
		keeper, ctx := keepertest.InferenceKeeper(t)
		keeper.SetEpochGroupData(ctx, types.EpochGroupData{EpochGroupId: epochId1})

		assert.NoError(t, keeper.DevelopersStatsSet(ctx, inference1Developer1)) // tagged to time now() - 3 sec
		assert.NoError(t, keeper.DevelopersStatsSet(ctx, inference2Developer1))
		assert.NoError(t, keeper.DevelopersStatsSet(ctx, inference1Developer2))

		now := time.Now().UTC()
		tokens, requests := keeper.CountTotalInferenceInPeriod(ctx, now.Add(-time.Second*10).Unix(), now.Add(-time.Second*2).Unix())
		assert.Equal(t,
			int64(inference1Developer1.CompletionTokenCount+inference1Developer1.PromptTokenCount),
			tokens)

		assert.Equal(t, 1, requests)
	})

	t.Run("count ai tokens and inference by epochs and developer", func(t *testing.T) {
		const currentEpochId = uint64(4)

		keeper, ctx := keepertest.InferenceKeeper(t)
		keeper.SetEpochGroupData(ctx, types.EpochGroupData{EpochGroupId: epochId1})
		assert.NoError(t, keeper.DevelopersStatsSet(ctx, inference1Developer1))

		keeper.SetEpochGroupData(ctx, types.EpochGroupData{EpochGroupId: epochId2})
		assert.NoError(t, keeper.DevelopersStatsSet(ctx, inference1Developer2))

		keeper.SetEpochGroupData(ctx, types.EpochGroupData{EpochGroupId: currentEpochId})
		assert.NoError(t, keeper.DevelopersStatsSet(ctx, inference2Developer1))

		tokensExpectedForLast2Epochs := inference1Developer2.PromptTokenCount + inference1Developer2.CompletionTokenCount

		tokens, requests := keeper.CountTotalInferenceInLastNEpochs(ctx, 2)
		assert.Equal(t, int64(tokensExpectedForLast2Epochs), tokens)
		assert.Equal(t, 1, requests)

		tokens, requests = keeper.CountTotalInferenceInLastNEpochs(ctx, 1)
		assert.Equal(t, int64(0), tokens)
		assert.Equal(t, 0, requests)

		tokens, requests = keeper.CountTotalInferenceInLastNEpochsByDeveloper(ctx, developer2, 2)
		assert.Equal(t, int64(inference1Developer2.PromptTokenCount+inference1Developer2.CompletionTokenCount), tokens)
		assert.Equal(t, 1, requests)

		tokens, requests = keeper.CountTotalInferenceInLastNEpochsByDeveloper(ctx, developer1, 3)
		assert.Equal(t, int64(inference1Developer1.PromptTokenCount+inference1Developer1.CompletionTokenCount), tokens)
		assert.Equal(t, 1, requests)

		tokens, requests = keeper.CountTotalInferenceInLastNEpochsByDeveloper(ctx, developer2, 1)
		assert.Equal(t, int64(0), tokens)
		assert.Equal(t, 0, requests)
	})
}
