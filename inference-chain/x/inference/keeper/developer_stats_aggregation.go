package keeper

import (
	"context"
	"cosmossdk.io/store/prefix"
	"errors"
	"github.com/cosmos/cosmos-sdk/runtime"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
	"golang.org/x/exp/maps"
)

const (
	DevelopersByEpoch     = "developers/epoch"
	DevelopersByTime      = "developers/time"
	DevelopersByInference = "developers/inference"
)

var ErrStatsByEpochNotFound = errors.New("stats by epoch not found")

func (k Keeper) DevelopersStatsSet(ctx context.Context, inference types.Inference) error {
	k.LogInfo("stat set BY TIME: got stat", types.Stat, "inference_id", inference.InferenceId, "inference_status", inference.Status.String(), "developer", inference.RequestedBy, "poc_block_height", inference.EpochGroupId)
	if inference.EpochGroupId == 0 {
		// we normally attach inference to group only when inference is finished.
		// But in that case it is not possible gather statistic by epoch properly, that's why we temporarily attach inference
		// to current epoch and then update it later.
		epoch, err := k.GetCurrentEpochGroup(ctx)
		if err != nil {
			return err
		}
		inference.EpochGroupId = epoch.GroupData.PocStartBlockHeight
		k.LogInfo("stat set: zero epoch Id", types.Stat, "inference_id", inference.InferenceId, "developer", inference.RequestedBy, "new_epoch_id", inference.EpochGroupId)
	}

	tokens := inference.CompletionTokenCount + inference.PromptTokenCount
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	inferenceStats := &types.InferenceStats{
		InferenceId:  inference.InferenceId,
		Status:       inference.Status,
		AiTokensUsed: tokens,
	}

	inferenceTime := inference.StartBlockTimestamp

	timeStore := prefix.NewStore(storeAdapter, types.KeyPrefix(DevelopersByTime))
	indexStore := prefix.NewStore(storeAdapter, types.KeyPrefix(DevelopersByInference))

	timeKey := indexStore.Get([]byte(inference.InferenceId))
	if timeKey == nil {
		// completely new record
		k.LogInfo("stat set BY TIME: completely new record, create record by time", types.Stat, "inference_id", inference.InferenceId, "developer", inference.RequestedBy)
		timeKey = developerByTimeKey(inference.RequestedBy, uint64(inferenceTime))
		timeStore.Set(timeKey, k.cdc.MustMarshal(&types.DeveloperStatsByTime{
			EpochId:   inference.EpochGroupId,
			Timestamp: inferenceTime,
			Inference: inferenceStats,
		}))
		indexStore.Set([]byte(inference.InferenceId), timeKey)
		return k.setStatByEpoch(ctx, inference, 0, tokens)
	}

	var statsByTime types.DeveloperStatsByTime
	var prevEpochId uint64
	if val := timeStore.Get(timeKey); val != nil {
		k.LogInfo("stat set BY TIME: record exists", types.Stat, "inference_id", inference.InferenceId, "developer", inference.RequestedBy)
		k.cdc.MustUnmarshal(val, &statsByTime)
		prevEpochId = statsByTime.EpochId
		statsByTime.EpochId = inference.EpochGroupId
	} else {
		k.LogInfo("stat set BY TIME: timekey exists, record DO NOT exist", types.Stat, "inference_id", inference.InferenceId, "developer", inference.RequestedBy)
		statsByTime = types.DeveloperStatsByTime{
			EpochId:   inference.EpochGroupId,
			Timestamp: inferenceTime,
		}
	}
	statsByTime.Inference = inferenceStats
	timeStore.Set(timeKey, k.cdc.MustMarshal(&statsByTime))
	indexStore.Set([]byte(inference.InferenceId), timeKey)
	return k.setStatByEpoch(ctx, inference, prevEpochId, tokens)
}

func (k Keeper) setStatByEpoch(
	ctx context.Context,
	inference types.Inference,
	previouslyKnownEpochId uint64,
	tokens uint64,
) error {
	k.LogInfo("stat set BY EPOCH", types.Stat, "inference_id", inference.InferenceId, "developer", inference.RequestedBy, "epoch_id", inference.EpochGroupId, "previously_known_epoch_id", previouslyKnownEpochId)
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	epochStore := prefix.NewStore(storeAdapter, types.KeyPrefix(DevelopersByEpoch))

	// === CASE 1: it is new record or update within same epoch ===
	if previouslyKnownEpochId == 0 {
		k.LogInfo("stat set BY EPOCH: new record or same epoch", types.Stat, "inference_id", inference.InferenceId, "developer", inference.RequestedBy, "epoch_id", inference.EpochGroupId)
		key := developerByEpochKey(inference.RequestedBy, inference.EpochGroupId)

		var stats types.DeveloperStatsByEpoch
		bz := epochStore.Get(key)
		if bz != nil {
			k.cdc.MustUnmarshal(bz, &stats)
		} else {
			stats = types.DeveloperStatsByEpoch{
				EpochId:    inference.EpochGroupId,
				Inferences: make(map[string]*types.InferenceStats),
			}
		}

		stats.Inferences[inference.InferenceId] = &types.InferenceStats{
			InferenceId:  inference.InferenceId,
			Status:       inference.Status,
			AiTokensUsed: tokens,
		}

		epochStore.Set(key, k.cdc.MustMarshal(&stats))
		return nil
	}

	// === CASE 2: inference already exists, but was tagged by different epoch ===
	if previouslyKnownEpochId != inference.EpochGroupId {
		k.LogInfo("stat set BY EPOCH: inference already exists, but was tagged by different epoch, clean up", types.Stat, "inference_id", inference.InferenceId, "developer", inference.RequestedBy, "epoch_id", inference.EpochGroupId)
		oldKey := developerByEpochKey(inference.RequestedBy, previouslyKnownEpochId)
		bz := epochStore.Get(oldKey)
		if bz == nil {
			return ErrStatsByEpochNotFound
		}

		var oldStats types.DeveloperStatsByEpoch
		k.cdc.MustUnmarshal(bz, &oldStats)
		delete(oldStats.Inferences, inference.InferenceId)
		epochStore.Set(oldKey, k.cdc.MustMarshal(&oldStats))
	}

	k.LogInfo("stat set BY EPOCH: add inference to epoch", types.Stat, "inference_id", inference.InferenceId, "developer", inference.RequestedBy, "epoch_id", inference.EpochGroupId)
	newKey := developerByEpochKey(inference.RequestedBy, inference.EpochGroupId)
	var newStats types.DeveloperStatsByEpoch
	bz := epochStore.Get(newKey)
	if bz != nil {
		k.cdc.MustUnmarshal(bz, &newStats)
	} else {
		newStats = types.DeveloperStatsByEpoch{
			EpochId: inference.EpochGroupId,
			Inferences: map[string]*types.InferenceStats{
				inference.InferenceId: {
					InferenceId: inference.InferenceId,
				},
			},
		}
	}

	statByInference, _ := newStats.Inferences[inference.InferenceId]
	statByInference.Status = inference.Status
	statByInference.AiTokensUsed = tokens
	epochStore.Set(newKey, k.cdc.MustMarshal(&newStats))

	k.LogInfo("stat set BY EPOCH: inference successfully added to epoch", types.Stat, "inference_id", inference.InferenceId, "developer", inference.RequestedBy, "epoch_id", inference.EpochGroupId)
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

	iter := epochStore.ReverseIterator(nil, sdk.Uint64ToBigEndian(currentEpoch.GroupData.EpochGroupId))
	defer iter.Close()

	var seenEpochs = make(map[uint64]bool)
	for ; iter.Valid(); iter.Next() {
		var stats types.DeveloperStatsByEpoch
		k.cdc.MustUnmarshal(iter.Value(), &stats)

		if len(maps.Keys(seenEpochs)) == n && !seenEpochs[stats.EpochId] {
			break
		}

		seenEpochs[stats.EpochId] = true
		for _, inf := range stats.Inferences {
			tokensTotal += int64(inf.AiTokensUsed)
			inferenceCount++
		}
	}
	return tokensTotal, inferenceCount
}

func (k Keeper) CountTotalInferenceInLastNEpochsByDeveloper(ctx context.Context, developerAddr string, n int) (int64, int) {
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

	iterator := epochStore.ReverseIterator(nil, sdk.Uint64ToBigEndian(currentEpoch.GroupData.EpochGroupId))
	defer iterator.Close()

	var (
		tokensTotal    int64
		inferenceCount int
	)

	var seenEpochs = make(map[uint64]bool)

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

		if len(maps.Keys(seenEpochs)) == n && !seenEpochs[stats.EpochId] {
			break
		}

		seenEpochs[stats.EpochId] = true
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
