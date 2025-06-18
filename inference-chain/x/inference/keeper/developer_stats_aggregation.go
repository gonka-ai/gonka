package keeper

import (
	"context"
	"cosmossdk.io/store/prefix"
	"github.com/cosmos/cosmos-sdk/runtime"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

const (
	DevelopersByEpoch             = "developers/epoch"
	DevelopersByTime              = "developers/time"
	DevelopersByInference         = "developers/inference"
	DevelopersByInferenceAndModel = "developers/inference/model"
)

type StatsSummary struct {
	InferenceCount int
	TokensUsed     int64
	ActualCost     int64
}

func (k Keeper) DevelopersStatsSet(ctx context.Context, inference types.Inference) error {
	k.LogInfo("stat set BY TIME: got stat", types.Stat, "inference_id", inference.InferenceId, "inference_status", inference.Status.String(), "developer", inference.RequestedBy, "poc_block_height", inference.EpochGroupId)
	epoch, err := k.GetCurrentEpochGroup(ctx)
	if err != nil {
		return err
	}

	currentEpochId := epoch.GroupData.EpochGroupId
	tokens := inference.CompletionTokenCount + inference.PromptTokenCount
	inferenceTime := inference.StartBlockTimestamp
	inferenceStats := &types.InferenceStats{
		EpochPocBlockHeight: inference.EpochGroupId,
		InferenceId:         inference.InferenceId,
		Status:              inference.Status,
		AiTokensUsed:        tokens,
		Model:               inference.Model,
		ActualConstInCoins:  inference.ActualCost,
	}

	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	byInferenceStore := prefix.NewStore(storeAdapter, types.KeyPrefix(DevelopersByInference))
	byTimeStore := prefix.NewStore(storeAdapter, types.KeyPrefix(DevelopersByTime))
	byModelsStore := prefix.NewStore(storeAdapter, types.KeyPrefix(DevelopersByInferenceAndModel))

	timeKey := byInferenceStore.Get([]byte(inference.InferenceId))
	if timeKey == nil {
		// completely new record
		k.LogInfo("stat set BY TIME: completely new record, create record by time", types.Stat, "inference_id", inference.InferenceId, "developer", inference.RequestedBy)
		timeKey = developerByTimeKey(inference.RequestedBy, uint64(inferenceTime))
		byTimeStore.Set(timeKey, k.cdc.MustMarshal(&types.DeveloperStatsByTime{
			EpochId:   currentEpochId,
			Timestamp: inferenceTime,
			Inference: inferenceStats,
		}))
		byInferenceStore.Set([]byte(inference.InferenceId), timeKey)
		modelKey := modelByTimeKey(inference.Model, inferenceTime, inference.InferenceId)
		byModelsStore.Set(modelKey, timeKey)
		return k.setStatByEpoch(ctx, inference, currentEpochId, 0, tokens)
	}

	var (
		statsByTime types.DeveloperStatsByTime
		prevEpochId uint64
	)
	if val := byTimeStore.Get(timeKey); val != nil {
		k.LogInfo("stat set BY TIME: record exists", types.Stat, "inference_id", inference.InferenceId, "developer", inference.RequestedBy)
		k.cdc.MustUnmarshal(val, &statsByTime)
		prevEpochId = statsByTime.EpochId

		statsByTime.EpochId = currentEpochId
		statsByTime.Inference.Status = inference.Status
		statsByTime.Inference.AiTokensUsed = tokens
		statsByTime.Inference.EpochPocBlockHeight = inference.EpochGroupId
		statsByTime.Inference.ActualConstInCoins = inference.ActualCost
	} else {
		k.LogInfo("stat set BY TIME: timekey exists, record DO NOT exist", types.Stat, "inference_id", inference.InferenceId, "developer", inference.RequestedBy)
		statsByTime = types.DeveloperStatsByTime{
			EpochId:   currentEpochId,
			Timestamp: inferenceTime,
			Inference: inferenceStats,
		}
	}
	byTimeStore.Set(timeKey, k.cdc.MustMarshal(&statsByTime))
	byInferenceStore.Set([]byte(inference.InferenceId), timeKey)

	modelKey := modelByTimeKey(inference.Model, inferenceTime, inference.InferenceId)
	byModelsStore.Set(modelKey, timeKey)
	return k.setStatByEpoch(ctx, inference, currentEpochId, prevEpochId, tokens)
}

func (k Keeper) setStatByEpoch(
	ctx context.Context,
	inference types.Inference,
	currentEpochId uint64,
	previouslyKnownEpochId uint64,
	tokens uint64,
) error {
	k.LogInfo("stat set BY EPOCH", types.Stat, "inference_id", inference.InferenceId, "developer", inference.RequestedBy, "epoch_id", currentEpochId, "previously_known_epoch_id", previouslyKnownEpochId)
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	epochStore := prefix.NewStore(storeAdapter, types.KeyPrefix(DevelopersByEpoch))

	// === CASE 1: inference already exists, but was tagged by different epoch ===
	if previouslyKnownEpochId != 0 && previouslyKnownEpochId != currentEpochId {
		k.LogInfo("stat set BY EPOCH: inference already exists, but was tagged by different epoch, clean up", types.Stat, "inference_id", inference.InferenceId, "developer", inference.RequestedBy, "epoch_id", currentEpochId)

		oldKey := developerByEpochKey(inference.RequestedBy, previouslyKnownEpochId)
		if bz := epochStore.Get(oldKey); bz != nil {
			var oldStats types.DeveloperStatsByEpoch
			k.cdc.MustUnmarshal(bz, &oldStats)

			delete(oldStats.Inferences, inference.InferenceId)
			epochStore.Set(oldKey, k.cdc.MustMarshal(&oldStats))
		}
	}

	// === CASE 2: create new record or update existing with current_epoch_id ===
	k.LogInfo("stat set BY EPOCH: new record or same epoch", types.Stat, "inference_id", inference.InferenceId, "developer", inference.RequestedBy, "epoch_id", currentEpochId)
	newKey := developerByEpochKey(inference.RequestedBy, currentEpochId)
	var newStats types.DeveloperStatsByEpoch
	if bz := epochStore.Get(newKey); bz != nil {
		k.cdc.MustUnmarshal(bz, &newStats)
		if newStats.Inferences == nil {
			newStats.Inferences = make(map[string]*types.InferenceStats)
		}
	} else {
		newStats = types.DeveloperStatsByEpoch{
			EpochId:    currentEpochId,
			Inferences: make(map[string]*types.InferenceStats),
		}
	}
	newStats.Inferences[inference.InferenceId] = &types.InferenceStats{
		EpochPocBlockHeight: inference.EpochGroupId,
		InferenceId:         inference.InferenceId,
		Status:              inference.Status,
		Model:               inference.Model,
		AiTokensUsed:        tokens,
		ActualConstInCoins:  inference.ActualCost,
	}
	epochStore.Set(newKey, k.cdc.MustMarshal(&newStats))
	k.LogInfo("stat set BY EPOCH: inference successfully added to epoch", types.Stat, "inference_id", inference.InferenceId, "developer", inference.RequestedBy, "epoch_id", currentEpochId)
	return nil
}

func (k Keeper) DevelopersStatsGetByEpoch(ctx context.Context, developerAddr string, epochId uint64) (types.DeveloperStatsByEpoch, bool) {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	epochStore := prefix.NewStore(store, types.KeyPrefix(DevelopersByEpoch))
	epochKey := developerByEpochKey(developerAddr, epochId)

	bz := epochStore.Get(epochKey)
	if bz == nil {
		return types.DeveloperStatsByEpoch{}, false
	}

	var stats types.DeveloperStatsByEpoch
	k.cdc.MustUnmarshal(bz, &stats)
	return stats, true
}

func (k Keeper) DevelopersStatsGetByTime(
	ctx context.Context,
	developerAddr string,
	timeFrom, timeTo int64,
) []*types.DeveloperStatsByTime {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	timeStore := prefix.NewStore(store, types.KeyPrefix(DevelopersByTime))

	var results []*types.DeveloperStatsByTime

	startKey := developerByTimeKey(developerAddr, uint64(timeFrom))
	endKey := developerByTimeKey(developerAddr, uint64(timeTo+1))

	iterator := timeStore.Iterator(startKey, endKey)
	defer iterator.Close()

	for ; iterator.Valid(); iterator.Next() {
		var stats types.DeveloperStatsByTime
		k.cdc.MustUnmarshal(iterator.Value(), &stats)
		results = append(results, &stats)
	}
	return results
}

func (k Keeper) CountTotalInferenceInPeriod(ctx context.Context, from, to int64) StatsSummary {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	timeStore := prefix.NewStore(store, types.KeyPrefix(DevelopersByTime))

	start := sdk.Uint64ToBigEndian(uint64(from))
	end := sdk.Uint64ToBigEndian(uint64(to + 1))

	iterator := timeStore.Iterator(start, end)
	defer iterator.Close()

	summary := StatsSummary{}
	for ; iterator.Valid(); iterator.Next() {
		var stats types.DeveloperStatsByTime
		k.cdc.MustUnmarshal(iterator.Value(), &stats)
		summary.TokensUsed += int64(stats.Inference.AiTokensUsed)
		summary.InferenceCount++
		summary.ActualCost += stats.Inference.ActualConstInCoins
	}
	return summary
}

func (k Keeper) CountTotalInferenceInLastNEpochs(ctx context.Context, n int) StatsSummary {
	if n <= 0 {
		return StatsSummary{}
	}

	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	epochStore := prefix.NewStore(store, types.KeyPrefix(DevelopersByEpoch))

	currentEpoch, err := k.GetCurrentEpochGroup(ctx)
	if err != nil {
		k.LogError("error getting current group id", types.Stat, "err", err.Error())
		return StatsSummary{}
	}

	epochIdFrom := currentEpoch.GroupData.EpochGroupId - uint64(n)
	epochIdTo := currentEpoch.GroupData.EpochGroupId

	iter := epochStore.Iterator(sdk.Uint64ToBigEndian(epochIdFrom), sdk.Uint64ToBigEndian(epochIdTo))
	defer iter.Close()

	summary := StatsSummary{}
	for ; iter.Valid(); iter.Next() {
		var stats types.DeveloperStatsByEpoch
		k.cdc.MustUnmarshal(iter.Value(), &stats)
		for _, inf := range stats.Inferences {
			summary.TokensUsed += int64(inf.AiTokensUsed)
			summary.InferenceCount++
			summary.ActualCost += inf.ActualConstInCoins
		}
	}
	return summary
}

func (k Keeper) CountTotalInferenceInLastNEpochsByDeveloper(ctx context.Context, developerAddr string, n int) StatsSummary {
	if n <= 0 {
		return StatsSummary{}
	}

	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	epochStore := prefix.NewStore(store, types.KeyPrefix(DevelopersByEpoch))

	currentEpoch, err := k.GetCurrentEpochGroup(ctx)
	if err != nil {
		k.LogError("error getting current group id", types.Stat, "err", err.Error())
		return StatsSummary{}
	}

	epochIdFrom := currentEpoch.GroupData.EpochGroupId - uint64(n)
	epochIdTo := currentEpoch.GroupData.EpochGroupId

	iterator := epochStore.Iterator(sdk.Uint64ToBigEndian(epochIdFrom), sdk.Uint64ToBigEndian(epochIdTo))
	defer iterator.Close()
	summary := StatsSummary{}
	for ; iterator.Valid(); iterator.Next() {
		key := iterator.Key()
		keyDeveloper := string(key[8:])
		if keyDeveloper != developerAddr {
			continue
		}

		var stats types.DeveloperStatsByEpoch
		k.cdc.MustUnmarshal(iterator.Value(), &stats)
		for _, inf := range stats.Inferences {
			summary.TokensUsed += int64(inf.AiTokensUsed)
			summary.InferenceCount++
			summary.ActualCost += inf.ActualConstInCoins
		}
	}
	return summary
}

func (k Keeper) GetStatsGroupedByModelAndTimePeriod(ctx context.Context, from, to int64) map[string]StatsSummary {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	timeStore := prefix.NewStore(store, types.KeyPrefix(DevelopersByTime))

	start := sdk.Uint64ToBigEndian(uint64(from))
	end := sdk.Uint64ToBigEndian(uint64(to + 1))

	iter := timeStore.Iterator(start, end)
	defer iter.Close()

	stats := make(map[string]StatsSummary)

	for ; iter.Valid(); iter.Next() {
		var stat types.DeveloperStatsByTime
		k.cdc.MustUnmarshal(iter.Value(), &stat)

		model := stat.Inference.Model
		s, ok := stats[model]
		if !ok {
			s = StatsSummary{}
		}
		s.InferenceCount++
		s.TokensUsed += int64(stat.Inference.AiTokensUsed)
		s.ActualCost += stat.Inference.ActualConstInCoins
		stats[model] = s
	}
	return stats
}

func (k Keeper) DumpAllDeveloperStats(ctx context.Context) (map[string]*types.DeveloperStatsByEpoch, []*types.DeveloperStatsByTime) {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))

	// === DeveloperStatsByEpoch ===
	epochStore := prefix.NewStore(store, types.KeyPrefix(DevelopersByEpoch))
	epochIter := epochStore.Iterator(nil, nil)
	defer epochIter.Close()

	epochStats := make(map[string]*types.DeveloperStatsByEpoch)
	for ; epochIter.Valid(); epochIter.Next() {
		var stats types.DeveloperStatsByEpoch
		k.cdc.MustUnmarshal(epochIter.Value(), &stats)
		epochStats[string(epochIter.Key())] = &stats
	}

	// === DeveloperStatsByTime ===
	timeStore := prefix.NewStore(store, types.KeyPrefix(DevelopersByTime))
	timeIter := timeStore.Iterator(nil, nil)
	defer timeIter.Close()

	var timeStats []*types.DeveloperStatsByTime
	for ; timeIter.Valid(); timeIter.Next() {
		var stats types.DeveloperStatsByTime
		k.cdc.MustUnmarshal(timeIter.Value(), &stats)
		timeStats = append(timeStats, &stats)
	}

	return epochStats, timeStats
}

func modelByTimeKey(model string, timestamp int64, inferenceId string) []byte {
	modelKey := append([]byte(model+"|"), sdk.Uint64ToBigEndian(uint64(timestamp))...)
	return append(modelKey, []byte(inferenceId)...)
}

func developerByEpochKey(developerAddr string, epochId uint64) []byte {
	return append(sdk.Uint64ToBigEndian(epochId), []byte(developerAddr)...)
}

func developerByTimeKey(developerAddr string, timestamp uint64) []byte {
	return append(sdk.Uint64ToBigEndian(timestamp), []byte(developerAddr)...)
}
