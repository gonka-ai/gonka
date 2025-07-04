package keeper_test

import (
	"github.com/cometbft/cometbft/crypto/secp256k1"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/google/uuid"
	keepertest "github.com/productscience/inference/testutil/keeper"
	keeper2 "github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/assert"
	"testing"
	"time"
)

func TestDeveloperStats_MultipleDevs_MultipleEpochs(t *testing.T) {
	const (
		testModel  = "test_model"
		testModel2 = "test_model_2"

		epochId1 = uint64(1)
		epochId2 = uint64(2)
		epochId3 = uint64(3)
		tokens   = uint64(10)
	)

	developer1 := sdk.AccAddress(secp256k1.GenPrivKey().PubKey().Address()).String()
	developer2 := sdk.AccAddress(secp256k1.GenPrivKey().PubKey().Address()).String()

	// Effective epoch = 1
	inference1Developer1 := types.Inference{
		InferenceId:          uuid.New().String(),
		PromptTokenCount:     tokens,
		CompletionTokenCount: tokens * 2,
		RequestedBy:          developer1,
		Status:               types.InferenceStatus_STARTED,
		Model:                testModel,
		StartBlockTimestamp:  time.Now().Add(-time.Second * 3).UnixMilli(),
		ActualCost:           1000,
	}

	inference2Developer1 := types.Inference{
		InferenceId:              uuid.New().String(),
		PromptTokenCount:         tokens,
		CompletionTokenCount:     tokens,
		RequestedBy:              developer1,
		Model:                    testModel2,
		Status:                   types.InferenceStatus_FINISHED,
		EpochPocStartBlockHeight: 0,
		EpochId:                  epochId2,
		StartBlockTimestamp:      time.Now().UnixMilli(),
		ActualCost:               1200,
	}

	inference1Developer2 := types.Inference{
		InferenceId:              uuid.New().String(),
		PromptTokenCount:         tokens * 3,
		CompletionTokenCount:     tokens,
		RequestedBy:              developer2,
		Status:                   types.InferenceStatus_FINISHED,
		Model:                    testModel2,
		EndBlockTimestamp:        time.Now().Add(-1 * time.Second).UnixMilli(),
		EpochPocStartBlockHeight: 0,
		EpochId:                  epochId1,
		ActualCost:               5000,
	}

	inference2Developer2 := types.Inference{
		InferenceId:          uuid.New().String(),
		PromptTokenCount:     tokens,
		CompletionTokenCount: tokens,
		RequestedBy:          developer2,
		Model:                testModel2,
		Status:               types.InferenceStatus_EXPIRED,
		StartBlockTimestamp:  time.Now().UnixMilli(),
		ActualCost:           1000,
	}

	keeper, ctx := keepertest.InferenceKeeper(t)

	keeper.SetEpoch(ctx, &types.Epoch{Index: epochId1, PocStartBlockHeight: int64(epochId1 * 10)})
	keeper.SetEffectiveEpochIndex(ctx, epochId1)

	assert.NoError(t, keeper.SetDeveloperStats(ctx, inference1Developer1)) // tagged to epoch 1
	assert.NoError(t, keeper.SetDeveloperStats(ctx, inference1Developer2)) // tagged to epoch 1

	keeper.SetEpoch(ctx, &types.Epoch{Index: epochId2, PocStartBlockHeight: int64(epochId2 * 10)})
	keeper.SetEffectiveEpochIndex(ctx, epochId2)
	assert.NoError(t, keeper.SetDeveloperStats(ctx, inference2Developer1)) // tagged to epoch 2

	keeper.SetEpoch(ctx, &types.Epoch{Index: epochId3, PocStartBlockHeight: int64(epochId3 * 10)})
	keeper.SetEffectiveEpochIndex(ctx, epochId3)
	assert.NoError(t, keeper.SetDeveloperStats(ctx, inference2Developer2)) // tagged to epoch 3

	defaultExpectedStatsByTime := map[string]*types.DeveloperStatsByTime{
		inference1Developer1.InferenceId: {
			EpochId:   epochId1,
			Timestamp: inference1Developer1.StartBlockTimestamp,
			Inference: &types.InferenceStats{
				InferenceId:       inference1Developer1.InferenceId,
				Status:            inference1Developer1.Status,
				TotalTokenCount:   inference1Developer1.PromptTokenCount + inference1Developer1.CompletionTokenCount,
				Model:             inference1Developer1.Model,
				ActualCostInCoins: inference1Developer1.ActualCost,
			},
		},
		inference2Developer1.InferenceId: {
			EpochId:   epochId2,
			Timestamp: inference2Developer1.StartBlockTimestamp,
			Inference: &types.InferenceStats{
				InferenceId:       inference2Developer1.InferenceId,
				EpochId:           inference2Developer1.EpochId,
				Status:            inference2Developer1.Status,
				TotalTokenCount:   inference2Developer1.PromptTokenCount + inference2Developer1.CompletionTokenCount,
				Model:             inference2Developer1.Model,
				ActualCostInCoins: inference2Developer1.ActualCost,
			},
		},
		inference1Developer2.InferenceId: {
			EpochId:   epochId1,
			Timestamp: inference1Developer2.EndBlockTimestamp,
			Inference: &types.InferenceStats{
				InferenceId:       inference1Developer2.InferenceId,
				EpochId:           inference1Developer2.EpochId,
				Status:            inference1Developer2.Status,
				TotalTokenCount:   inference1Developer2.PromptTokenCount + inference1Developer2.CompletionTokenCount,
				Model:             inference1Developer2.Model,
				ActualCostInCoins: inference1Developer2.ActualCost,
			},
		},
		inference2Developer2.InferenceId: {
			EpochId:   epochId3,
			Timestamp: inference2Developer2.StartBlockTimestamp,
			Inference: &types.InferenceStats{
				InferenceId:       inference2Developer2.InferenceId,
				Status:            inference2Developer2.Status,
				TotalTokenCount:   inference2Developer2.PromptTokenCount + inference2Developer2.CompletionTokenCount,
				Model:             inference2Developer2.Model,
				ActualCostInCoins: inference2Developer2.ActualCost,
			},
		},
	}

	t.Run("get statistic by time and developer", func(t *testing.T) {
		// get all stat by developer1
		dev1Stats := keeper.GetDeveloperStatsByTime(ctx, developer1, inference1Developer1.StartBlockTimestamp-10, time.Now().UnixMilli())
		assert.Len(t, dev1Stats, 2)

		expectedInferenceId := map[string]struct{}{inference1Developer1.InferenceId: {}, inference2Developer1.InferenceId: {}}
		for _, stat := range dev1Stats {
			_, ok := expectedInferenceId[stat.Inference.InferenceId]
			assert.True(t, ok)

			val, ok := defaultExpectedStatsByTime[stat.Inference.InferenceId]
			assert.True(t, ok)
			assert.Equal(t, *val, *stat)
		}

		// get all stat by developer2
		dev2Stats := keeper.GetDeveloperStatsByTime(ctx, developer2, inference1Developer1.StartBlockTimestamp-10, time.Now().UnixMilli())
		assert.Len(t, dev1Stats, 2)

		expectedInferenceId = map[string]struct{}{inference1Developer2.InferenceId: {}, inference2Developer2.InferenceId: {}}
		for _, stat := range dev2Stats {
			_, ok := expectedInferenceId[stat.Inference.InferenceId]
			assert.True(t, ok)

			val, ok := defaultExpectedStatsByTime[stat.Inference.InferenceId]
			assert.True(t, ok)
			assert.Equal(t, *val, *stat)
		}

		// get earliest stat by developer1
		dev1StatsEarliest := keeper.GetDeveloperStatsByTime(ctx, developer1, inference1Developer1.StartBlockTimestamp-10, inference1Developer1.StartBlockTimestamp+10)
		assert.Len(t, dev1StatsEarliest, 1)

		val, ok := defaultExpectedStatsByTime[dev1StatsEarliest[0].Inference.InferenceId]
		assert.True(t, ok)
		assert.Equal(t, *val, *dev1StatsEarliest[0])

		// get earliest stat by developer2
		dev2StatsEarliest := keeper.GetDeveloperStatsByTime(ctx, developer2, inference1Developer2.EndBlockTimestamp-10, inference1Developer2.EndBlockTimestamp+10)
		assert.Len(t, dev2StatsEarliest, 1)

		val, ok = defaultExpectedStatsByTime[dev2StatsEarliest[0].Inference.InferenceId]
		assert.True(t, ok)
		assert.Equal(t, *val, *dev2StatsEarliest[0])
	})

	t.Run("inferences by time not found", func(t *testing.T) {
		now := time.Now()
		statsByTime := keeper.GetDeveloperStatsByTime(ctx, developer1, now.Add(-time.Minute*2).UnixMilli(), now.Add(-time.Minute).UnixMilli())
		assert.Empty(t, statsByTime)
		statsByTime = keeper.GetDeveloperStatsByTime(ctx, developer2, now.Add(-time.Minute*2).UnixMilli(), now.Add(-time.Minute).UnixMilli())
		assert.Empty(t, statsByTime)
	})

	t.Run("count totals by n epochs and developer backwards not including current epoch", func(t *testing.T) {
		// current epoch = 3
		summary := keeper.GetSummaryLastNEpochsByDeveloper(ctx, developer1, 1)
		assert.Equal(t, keeper2.StatsSummary{
			InferenceCount: 1,
			TokensUsed:     int64(inference2Developer1.CompletionTokenCount + inference2Developer1.PromptTokenCount),
			ActualCost:     inference2Developer1.ActualCost,
		}, summary)

		summary = keeper.GetSummaryLastNEpochsByDeveloper(ctx, developer1, 2)
		assert.Equal(t, keeper2.StatsSummary{
			InferenceCount: 2,
			TokensUsed:     int64(inference2Developer1.CompletionTokenCount + inference2Developer1.PromptTokenCount + inference1Developer1.CompletionTokenCount + inference1Developer1.PromptTokenCount),
			ActualCost:     inference2Developer1.ActualCost + inference1Developer1.ActualCost,
		}, summary)

		summary = keeper.GetSummaryLastNEpochsByDeveloper(ctx, developer1, 3)
		assert.Equal(t, keeper2.StatsSummary{
			InferenceCount: 2,
			TokensUsed:     int64(inference2Developer1.CompletionTokenCount + inference2Developer1.PromptTokenCount + inference1Developer1.CompletionTokenCount + inference1Developer1.PromptTokenCount),
			ActualCost:     inference2Developer1.ActualCost + inference1Developer1.ActualCost,
		}, summary)
	})

	t.Run("count totals by n epochs backwards not including current epoch", func(t *testing.T) {
		inferences := []types.Inference{inference1Developer1, inference2Developer1, inference1Developer2}
		expectedSum := keeper2.StatsSummary{}
		for _, inf := range inferences {
			expectedSum.InferenceCount++
			expectedSum.ActualCost += inf.ActualCost
			expectedSum.TokensUsed += int64(inf.PromptTokenCount + inf.CompletionTokenCount)
		}

		summary := keeper.GetSummaryLastNEpochs(ctx, 2)
		assert.Equal(t, expectedSum, summary)

		summary = keeper.GetSummaryLastNEpochs(ctx, 1)
		assert.Equal(t, keeper2.StatsSummary{
			InferenceCount: 1,
			TokensUsed:     int64(inference2Developer1.PromptTokenCount + inference2Developer1.CompletionTokenCount),
			ActualCost:     inference2Developer1.ActualCost,
		}, summary)
	})

	t.Run("count totals by time", func(t *testing.T) {
		inferences := []types.Inference{inference1Developer1, inference2Developer1, inference1Developer2, inference2Developer2}
		expectedSum := keeper2.StatsSummary{}
		for _, inf := range inferences {
			expectedSum.InferenceCount++
			expectedSum.ActualCost += inf.ActualCost
			expectedSum.TokensUsed += int64(inf.PromptTokenCount + inf.CompletionTokenCount)
		}
		summary := keeper.GetSummaryByTime(ctx, inference1Developer1.StartBlockTimestamp-10, time.Now().UnixMilli())
		assert.Equal(t, expectedSum, summary)

		// get earliest inference
		summary = keeper.GetSummaryByTime(ctx, inference1Developer1.StartBlockTimestamp-10, inference1Developer1.StartBlockTimestamp+10)
		assert.Equal(t, inference1Developer1.PromptTokenCount+inference1Developer1.CompletionTokenCount, uint64(summary.TokensUsed))
		assert.Equal(t, inference1Developer1.ActualCost, summary.ActualCost)
		assert.Equal(t, 1, summary.InferenceCount)

		// get 3 last inferences summary
		expectedSum.InferenceCount--
		expectedSum.ActualCost -= inference1Developer1.ActualCost
		expectedSum.TokensUsed -= int64(inference1Developer1.PromptTokenCount + inference1Developer1.CompletionTokenCount)

		summary = keeper.GetSummaryByTime(ctx, inference1Developer1.StartBlockTimestamp+10, time.Now().UnixMilli())
		assert.Equal(t, expectedSum, summary)
	})

	t.Run("count tokens per model", func(t *testing.T) {
		inferences := []types.Inference{inference1Developer1, inference2Developer1, inference1Developer2, inference2Developer2}
		expectedStats := make(map[string]keeper2.StatsSummary)
		for _, inf := range inferences {
			stat, ok := expectedStats[inf.Model]
			if !ok {
				stat = keeper2.StatsSummary{}
			}
			stat.InferenceCount++
			stat.ActualCost += inf.ActualCost
			stat.TokensUsed += int64(inf.PromptTokenCount + inf.CompletionTokenCount)
			expectedStats[inf.Model] = stat
		}

		stat := keeper.GetSummaryByModelAndTime(ctx, inference1Developer1.StartBlockTimestamp-10, time.Now().UnixMilli())
		assert.Equal(t, len(expectedStats), len(stat))
		for model, expectedStat := range expectedStats {
			actualStat := stat[model]
			assert.Equal(t, expectedStat, actualStat)
		}
	})
}

func TestDeveloperStats_OneDev(t *testing.T) {
	const (
		developer1 = "developer1"
		testModel  = "test_model"
		tokens     = uint64(10)
		epochId1   = uint64(1)
		epochId2   = uint64(2)
		epochId3   = uint64(3)
	)

	t.Run("inferences with zero start_timestamp and same end_timestamp, epoch and developer", func(t *testing.T) {
		keeper, ctx := keepertest.InferenceKeeper(t)
		keeper.SetEpoch(ctx, &types.Epoch{Index: epochId1, PocStartBlockHeight: int64(epochId1 * 10)})
		keeper.SetEffectiveEpochIndex(ctx, epochId1)

		now := time.Now().UnixMilli()

		inference1 := types.Inference{
			InferenceId:          "inferenceId1",
			PromptTokenCount:     tokens,
			CompletionTokenCount: tokens * 2,
			RequestedBy:          developer1,
			Status:               types.InferenceStatus_FINISHED,
			Model:                testModel,
			EndBlockTimestamp:    now,
			ActualCost:           1000,
		}

		inference2 := types.Inference{
			InferenceId:          "inferenceId2",
			PromptTokenCount:     tokens,
			CompletionTokenCount: tokens,
			RequestedBy:          developer1,
			Model:                testModel,
			Status:               types.InferenceStatus_FINISHED,
			EndBlockTimestamp:    now,
			ActualCost:           1200,
		}

		assert.NoError(t, keeper.SetDeveloperStats(ctx, inference1))
		assert.NoError(t, keeper.SetDeveloperStats(ctx, inference2))

		statsByTime := keeper.GetDeveloperStatsByTime(ctx, developer1, time.Now().Add(-time.Second*2).UnixMilli(), time.Now().UnixMilli())
		assert.Equal(t, 2, len(statsByTime))
		assert.Equal(t, statsByTime[0].Inference, &types.InferenceStats{
			InferenceId:       inference1.InferenceId,
			Status:            inference1.Status,
			TotalTokenCount:   inference1.PromptTokenCount + inference1.CompletionTokenCount,
			Model:             inference1.Model,
			ActualCostInCoins: inference1.ActualCost,
		})
		assert.Equal(t, statsByTime[1].Inference, &types.InferenceStats{
			InferenceId:       inference2.InferenceId,
			Status:            inference2.Status,
			TotalTokenCount:   inference2.PromptTokenCount + inference2.CompletionTokenCount,
			Model:             inference2.Model,
			ActualCostInCoins: inference2.ActualCost,
		})
	})

	t.Run("update same inference", func(t *testing.T) {
		keeper, ctx := keepertest.InferenceKeeper(t)
		keeper.SetEpoch(ctx, &types.Epoch{Index: epochId1, PocStartBlockHeight: int64(epochId1 * 10)})
		keeper.SetEffectiveEpochIndex(ctx, epochId1)

		inference := types.Inference{
			InferenceId:          "inferenceId1",
			PromptTokenCount:     tokens,
			CompletionTokenCount: tokens * 2,
			RequestedBy:          developer1,
			Status:               types.InferenceStatus_STARTED,
			Model:                testModel,
			StartBlockTimestamp:  time.Now().UnixMilli(),
		}

		expectedStatsBeforeUpdate := types.InferenceStats{
			InferenceId:     inference.InferenceId,
			EpochId:         inference.EpochId,
			Status:          inference.Status,
			TotalTokenCount: inference.PromptTokenCount + inference.CompletionTokenCount,
			Model:           inference.Model,
		}
		assert.NoError(t, keeper.SetDeveloperStats(ctx, inference))

		stat := keeper.GetDeveloperStatsByTime(ctx, developer1, inference.StartBlockTimestamp-10, inference.StartBlockTimestamp+10)
		assert.Equal(t, expectedStatsBeforeUpdate, *stat[0].Inference)
		assert.Equal(t, inference.StartBlockTimestamp, stat[0].Timestamp)
		assert.Equal(t, epochId1, stat[0].EpochId)

		// update inference
		actualCost := int64(10000)
		inference.ActualCost = actualCost
		inference.Status = types.InferenceStatus_FINISHED
		inference.EpochPocStartBlockHeight = 0
		inference.EpochId = epochId2
		inference.EndBlockTimestamp = time.Now().Add(5 * time.Second).UnixMilli()

		keeper.SetEpoch(ctx, &types.Epoch{Index: epochId2, PocStartBlockHeight: int64(epochId2 * 10)})
		keeper.SetEffectiveEpochIndex(ctx, epochId2)
		assert.NoError(t, keeper.SetDeveloperStats(ctx, inference))

		expectedStatsAfterUpdate := types.InferenceStats{
			InferenceId:       inference.InferenceId,
			EpochId:           epochId2,
			Status:            types.InferenceStatus_FINISHED,
			TotalTokenCount:   inference.PromptTokenCount + inference.CompletionTokenCount,
			Model:             inference.Model,
			ActualCostInCoins: actualCost,
		}

		stat = keeper.GetDeveloperStatsByTime(ctx, developer1, inference.EndBlockTimestamp-10, inference.EndBlockTimestamp+10)
		assert.Equal(t, expectedStatsAfterUpdate, *stat[0].Inference)
		assert.Equal(t, inference.EndBlockTimestamp, stat[0].Timestamp)
		assert.Equal(t, epochId2, stat[0].EpochId)

		stat = keeper.GetDeveloperStatsByTime(ctx, developer1, inference.StartBlockHeight-10, inference.StartBlockHeight+10)
		assert.Empty(t, stat)
	})

	// In case we received only FinishInference transaction,
	//  we won't know who the developer is
	t.Run("inference without developer address", func(t *testing.T) {
		keeper, ctx := keepertest.InferenceKeeper(t)
		keeper.SetEpoch(ctx, &types.Epoch{Index: epochId1, PocStartBlockHeight: int64(epochId1 * 10)})
		keeper.SetEpoch(ctx, &types.Epoch{Index: epochId2, PocStartBlockHeight: int64(epochId2 * 10)})
		keeper.SetEffectiveEpochIndex(ctx, epochId3)

		inference := types.Inference{
			InferenceId:              uuid.New().String(),
			PromptTokenCount:         tokens,
			CompletionTokenCount:     tokens * 2,
			EpochPocStartBlockHeight: epochId2 * 10,
			EpochId:                  epochId2,
			RequestedBy:              "",
			Status:                   types.InferenceStatus_FINISHED,
			Model:                    testModel,
			StartBlockTimestamp:      time.Now().UnixMilli(),
			EndBlockTimestamp:        time.Now().Add(5 * time.Second).UnixMilli(),
			ActualCost:               5000,
		}

		inference2 := types.Inference{
			InferenceId:              uuid.New().String(),
			PromptTokenCount:         tokens * 2,
			CompletionTokenCount:     tokens * 2,
			EpochPocStartBlockHeight: epochId2 * 10,
			EpochId:                  epochId2,
			RequestedBy:              developer1,
			Status:                   types.InferenceStatus_FINISHED,
			Model:                    testModel,
			StartBlockTimestamp:      time.Now().UnixMilli(),
			EndBlockTimestamp:        time.Now().Add(5 * time.Second).UnixMilli(),
			ActualCost:               7000,
		}

		assert.NoError(t, keeper.SetDeveloperStats(ctx, inference))
		assert.NoError(t, keeper.SetDeveloperStats(ctx, inference2))

		expectedSummary := keeper2.StatsSummary{
			InferenceCount: 1,
			TokensUsed:     int64(inference2.PromptTokenCount + inference2.CompletionTokenCount),
			ActualCost:     inference2.ActualCost,
		}

		summary := keeper.GetSummaryLastNEpochs(ctx, 1)
		assert.Equal(t, expectedSummary, summary)

		summary2 := keeper.GetSummaryByTime(ctx, inference.EndBlockTimestamp-10, inference2.EndBlockTimestamp+20)
		assert.Equal(t, expectedSummary, summary2)
	})
}
