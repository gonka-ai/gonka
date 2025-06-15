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

		testModel  = "test_model"
		testModel2 = "test_model_2"

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
		Model:                testModel,
		StartBlockTimestamp:  time.Now().Add(-time.Second * 3).UnixMilli(),
	}

	inference2Developer1 := types.Inference{
		InferenceId:          uuid.New().String(),
		PromptTokenCount:     tokens,
		CompletionTokenCount: tokens,
		RequestedBy:          developer1,
		Model:                testModel2,
		Status:               types.InferenceStatus_VALIDATED,
		StartBlockTimestamp:  time.Now().UnixMilli(),
	}

	inference1Developer2 := types.Inference{
		InferenceId:          uuid.New().String(),
		PromptTokenCount:     tokens * 3,
		CompletionTokenCount: tokens,
		RequestedBy:          developer2,
		Status:               types.InferenceStatus_FINISHED,
		Model:                testModel2,
		StartBlockTimestamp:  time.Now().UnixMilli(),
		EpochGroupId:         epochId2,
	}

	inference2Developer2 := types.Inference{
		InferenceId:          uuid.New().String(),
		PromptTokenCount:     tokens,
		CompletionTokenCount: tokens,
		RequestedBy:          developer2,
		Model:                testModel2,
		Status:               types.InferenceStatus_EXPIRED,
		StartBlockTimestamp:  time.Now().UnixMilli(),
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

		now := time.Now()
		statsByTime := keeper.DevelopersStatsGetByTime(ctx, developer1, now.Add(-time.Second).UnixMilli(), now.UnixMilli())
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

		now := time.Now()
		statsByTime := keeper.DevelopersStatsGetByTime(ctx, developer1, now.Add(-time.Second*4).UnixMilli(), now.UnixMilli())
		assert.Equal(t, 1, len(statsByTime))
		assert.Equal(t, updatedInference.Status, statsByTime[0].Inference.Status)
		assert.Equal(t, updatedInference.PromptTokenCount+updatedInference.CompletionTokenCount, statsByTime[0].Inference.AiTokensUsed)
	})

	t.Run("inferences by time not found", func(t *testing.T) {
		keeper, ctx := keepertest.InferenceKeeper(t)

		assert.NoError(t, keeper.DevelopersStatsSet(ctx, inference1Developer1))

		now := time.Now()
		statsByTime := keeper.DevelopersStatsGetByTime(ctx, developer1, now.Add(-time.Minute*2).UnixMilli(), now.Add(-time.Minute).UnixMilli())
		assert.Empty(t, statsByTime)
	})

	t.Run("count ai tokens and inference requests by time", func(t *testing.T) {
		keeper, ctx := keepertest.InferenceKeeper(t)
		keeper.SetEpochGroupData(ctx, types.EpochGroupData{EpochGroupId: epochId1})

		assert.NoError(t, keeper.DevelopersStatsSet(ctx, inference1Developer1)) // tagged to time now() - 3 sec
		assert.NoError(t, keeper.DevelopersStatsSet(ctx, inference2Developer1))
		assert.NoError(t, keeper.DevelopersStatsSet(ctx, inference1Developer2))

		now := time.Now()
		summary := keeper.CountTotalInferenceInPeriod(ctx, now.Add(-time.Second*10).UnixMilli(), now.Add(-time.Second*2).UnixMilli())
		assert.Equal(t, int64(inference1Developer1.CompletionTokenCount+inference1Developer1.PromptTokenCount), summary.TokensUsed)
		assert.Equal(t, 1, summary.InferenceCount)
	})

	t.Run("count ai tokens and inference requests by time and model", func(t *testing.T) {
		keeper, ctx := keepertest.InferenceKeeper(t)
		keeper.SetEpochGroupData(ctx, types.EpochGroupData{EpochGroupId: epochId1})

		assert.NoError(t, keeper.DevelopersStatsSet(ctx, inference1Developer1)) // tagged to time now() - 3 sec
		assert.NoError(t, keeper.DevelopersStatsSet(ctx, inference2Developer1))
		assert.NoError(t, keeper.DevelopersStatsSet(ctx, inference1Developer2))

		now := time.Now().UTC()
		summary := keeper.GetStatsGroupedByModelAndTimePeriod(ctx, now.Add(-time.Second*10).UnixMilli(), now.Add(-time.Second*2).UnixMilli())
		assert.Equal(t, 1, len(summary))

		stat, ok := summary[inference1Developer1.Model]
		assert.True(t, ok)
		assert.Equal(t, int64(inference1Developer1.CompletionTokenCount+inference1Developer1.PromptTokenCount), stat.TokensUsed)
		assert.Equal(t, 1, stat.InferenceCount)

		summary = keeper.GetStatsGroupedByModelAndTimePeriod(ctx, now.Add(-time.Second*1).UnixMilli(), now.Add(time.Second).UnixMilli())
		stat, ok = summary[testModel2]
		assert.True(t, ok)

		assert.Equal(t, int64(inference2Developer1.CompletionTokenCount+inference2Developer1.PromptTokenCount+inference1Developer2.CompletionTokenCount+inference1Developer2.PromptTokenCount), stat.TokensUsed)
		assert.Equal(t, 2, stat.InferenceCount)
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

		summary := keeper.CountTotalInferenceInLastNEpochs(ctx, 2)
		assert.Equal(t, int64(tokensExpectedForLast2Epochs), summary.TokensUsed)
		assert.Equal(t, 1, summary.InferenceCount)

		summary = keeper.CountTotalInferenceInLastNEpochs(ctx, 1)
		assert.Equal(t, int64(0), summary.TokensUsed)
		assert.Equal(t, 0, summary.InferenceCount)

		summary = keeper.CountTotalInferenceInLastNEpochsByDeveloper(ctx, developer2, 2)
		assert.Equal(t, int64(inference1Developer2.PromptTokenCount+inference1Developer2.CompletionTokenCount), summary.TokensUsed)
		assert.Equal(t, 1, summary.InferenceCount)

		summary = keeper.CountTotalInferenceInLastNEpochsByDeveloper(ctx, developer1, 3)
		assert.Equal(t, int64(inference1Developer1.PromptTokenCount+inference1Developer1.CompletionTokenCount), summary.TokensUsed)
		assert.Equal(t, 1, summary.InferenceCount)

		summary = keeper.CountTotalInferenceInLastNEpochsByDeveloper(ctx, developer2, 1)
		assert.Equal(t, int64(0), summary.TokensUsed)
		assert.Equal(t, 0, summary.InferenceCount)
	})
}
