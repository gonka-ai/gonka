package keeper

import (
	"context"
	"cosmossdk.io/store/prefix"
	"github.com/cosmos/cosmos-sdk/runtime"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

const (
	DevelopersByEpoch     = "developers/epoch"
	DevelopersByTime      = "developers/time"
	DevelopersByInference = "developers/inference"
)

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
	}

	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	timeStore := prefix.NewStore(storeAdapter, types.KeyPrefix(DevelopersByTime))
	indexStore := prefix.NewStore(storeAdapter, types.KeyPrefix(DevelopersByInference))

	timeKey := indexStore.Get([]byte(inference.InferenceId))
	if timeKey == nil {
		// completely new record
		k.LogInfo("stat set BY TIME: completely new record, create record by time", types.Stat, "inference_id", inference.InferenceId, "developer", inference.RequestedBy)
		timeKey = developerByTimeKey(inference.RequestedBy, uint64(inferenceTime))
		timeStore.Set(timeKey, k.cdc.MustMarshal(&types.DeveloperStatsByTime{
			EpochId:   currentEpochId,
			Timestamp: inferenceTime,
			Inference: inferenceStats,
		}))
		indexStore.Set([]byte(inference.InferenceId), timeKey)
		return k.setStatByEpoch(ctx, inference, currentEpochId, 0, tokens)
	}

	var (
		statsByTime types.DeveloperStatsByTime
		prevEpochId uint64
	)
	if val := timeStore.Get(timeKey); val != nil {
		k.LogInfo("stat set BY TIME: record exists", types.Stat, "inference_id", inference.InferenceId, "developer", inference.RequestedBy)
		k.cdc.MustUnmarshal(val, &statsByTime)
		prevEpochId = statsByTime.EpochId
		statsByTime.EpochId = currentEpochId
	} else {
		k.LogInfo("stat set BY TIME: timekey exists, record DO NOT exist", types.Stat, "inference_id", inference.InferenceId, "developer", inference.RequestedBy)
		statsByTime = types.DeveloperStatsByTime{
			EpochId:   currentEpochId,
			Timestamp: inferenceTime,
		}
	}
	statsByTime.Inference = inferenceStats
	timeStore.Set(timeKey, k.cdc.MustMarshal(&statsByTime))
	indexStore.Set([]byte(inference.InferenceId), timeKey)
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
		AiTokensUsed:        tokens,
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

func (k Keeper) CountTotalInferenceInPeriod(ctx context.Context, from, to int64) (aiTokesTotal int64, inferencesRequestsNum int) {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	timeStore := prefix.NewStore(store, types.KeyPrefix(DevelopersByTime))

	start := sdk.Uint64ToBigEndian(uint64(from))
	end := sdk.Uint64ToBigEndian(uint64(to + 1))

	iterator := timeStore.Iterator(start, end)
	defer iterator.Close()

	for ; iterator.Valid(); iterator.Next() {
		var stats types.DeveloperStatsByTime
		k.cdc.MustUnmarshal(iterator.Value(), &stats)
		aiTokesTotal += int64(stats.Inference.AiTokensUsed)
		inferencesRequestsNum++
	}
	return aiTokesTotal, inferencesRequestsNum
}

func (k Keeper) CountTotalInferenceInLastNEpochs(ctx context.Context, n int) (tokensTotal int64, inferenceCount int) {
	if n <= 0 {
		return 0, 0
	}

	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	epochStore := prefix.NewStore(store, types.KeyPrefix(DevelopersByEpoch))

	currentEpoch, err := k.GetCurrentEpochGroup(ctx)
	if err != nil {
		k.LogError("error getting current group id", types.Stat, "err", err.Error())
		return 0, 0
	}

	epochIdFrom := currentEpoch.GroupData.EpochGroupId - uint64(n)
	epochIdTo := currentEpoch.GroupData.EpochGroupId

	iter := epochStore.Iterator(sdk.Uint64ToBigEndian(epochIdFrom), sdk.Uint64ToBigEndian(epochIdTo))
	defer iter.Close()

	for ; iter.Valid(); iter.Next() {
		var stats types.DeveloperStatsByEpoch
		k.cdc.MustUnmarshal(iter.Value(), &stats)
		for _, inf := range stats.Inferences {
			tokensTotal += int64(inf.AiTokensUsed)
			inferenceCount++
		}
	}
	return tokensTotal, inferenceCount
}

func (k Keeper) CountTotalInferenceInLastNEpochsByDeveloper(ctx context.Context, developerAddr string, n int) (tokensTotal int64, inferenceCount int) {
	if n <= 0 {
		return 0, 0
	}

	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	epochStore := prefix.NewStore(store, types.KeyPrefix(DevelopersByEpoch))

	currentEpoch, err := k.GetCurrentEpochGroup(ctx)
	if err != nil {
		k.LogError("error getting current group id", types.Stat, "err", err.Error())
		return 0, 0
	}

	epochIdFrom := currentEpoch.GroupData.EpochGroupId - uint64(n)
	epochIdTo := currentEpoch.GroupData.EpochGroupId

	iterator := epochStore.Iterator(sdk.Uint64ToBigEndian(epochIdFrom), sdk.Uint64ToBigEndian(epochIdTo))
	defer iterator.Close()
	for ; iterator.Valid(); iterator.Next() {
		key := iterator.Key()
		if len(key) < 8 {
			continue
		}

		keyDeveloper := string(key[8:])
		if keyDeveloper != developerAddr {
			continue
		}

		var stats types.DeveloperStatsByEpoch
		k.cdc.MustUnmarshal(iterator.Value(), &stats)
		for _, inf := range stats.Inferences {
			tokensTotal += int64(inf.AiTokensUsed)
			inferenceCount++
		}
	}
	return tokensTotal, inferenceCount
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

func developerByEpochKey(developerAddr string, epochId uint64) []byte {
	return append(sdk.Uint64ToBigEndian(epochId), []byte(developerAddr)...)
}

func developerByTimeKey(developerAddr string, timestamp uint64) []byte {
	return append(sdk.Uint64ToBigEndian(timestamp), []byte(developerAddr)...)
}
