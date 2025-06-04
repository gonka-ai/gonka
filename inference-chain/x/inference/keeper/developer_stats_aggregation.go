package keeper

import (
	"context"
	"cosmossdk.io/store/prefix"
	"errors"
	"github.com/cosmos/cosmos-sdk/runtime"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
	"time"
)

const (
	DevelopersByEpoch     = "developers/epoch"
	DevelopersByTime      = "developers/time"
	DevelopersByInference = "developers/inference"
)

var ErrStatsByEpochNotFound = errors.New("stats by epoch not found")

func (k Keeper) DevelopersStatsSet(ctx context.Context, developerAddr string, inferenceID string, inferenceStatus types.InferenceStatus, epochID uint64, tokens uint64) error {
	if epochID == 0 {
		// we normally attach inference to group only when inference is finished.
		// But in that case it is not possible gather statistic by epoch properly, that's why we temporarily attach inference
		// to current epoch and then update it later.
		epoch, err := k.GetCurrentEpochGroup(ctx)
		if err != nil {
			return err
		}
		epochID = epoch.GroupData.EpochGroupId
	}

	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	inferenceStats := &types.InferenceStats{
		Status:       inferenceStatus,
		AiTokensUsed: tokens,
	}

	inferenceTime := time.Now().UTC().Truncate(time.Second).Unix()

	timeStore := prefix.NewStore(storeAdapter, types.KeyPrefix(DevelopersByTime))
	indexStore := prefix.NewStore(storeAdapter, types.KeyPrefix(DevelopersByInference))

	timeKey := indexStore.Get([]byte(inferenceID))
	if timeKey == nil {
		// completely new record
		timeKey = developerByTimeKey(developerAddr, uint64(inferenceTime))
		timeStore.Set(timeKey, k.cdc.MustMarshal(&types.DeveloperStatsByTime{
			EpochId:   epochID,
			Timestamp: inferenceTime,
			Inference: inferenceStats,
		}))
		indexStore.Set([]byte(inferenceID), timeKey)
		return k.setStatByEpoch(ctx, developerAddr, inferenceID, inferenceStatus, epochID, 0, tokens)
	}

	var statsByTime types.DeveloperStatsByTime
	var prevEpochId uint64
	if val := timeStore.Get(timeKey); val != nil {
		k.cdc.MustUnmarshal(val, &statsByTime)
		prevEpochId = statsByTime.EpochId
	} else {
		statsByTime = types.DeveloperStatsByTime{
			EpochId:   epochID,
			Timestamp: inferenceTime,
		}
	}
	statsByTime.Inference = inferenceStats
	timeStore.Set(timeKey, k.cdc.MustMarshal(&statsByTime))
	indexStore.Set([]byte(inferenceID), timeKey)
	return k.setStatByEpoch(ctx, developerAddr, inferenceID, inferenceStatus, epochID, prevEpochId, tokens)
}

func (k Keeper) setStatByEpoch(
	ctx context.Context,
	developerAddr string,
	inferenceID string,
	inferenceStatus types.InferenceStatus,
	epochID uint64,
	previouslyKnownEpochId uint64,
	tokens uint64,
) error {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	epochStore := prefix.NewStore(storeAdapter, types.KeyPrefix(DevelopersByEpoch))

	// === CASE 1: it is new record ===
	if previouslyKnownEpochId == 0 {
		key := developerByEpochKey(developerAddr, epochID)

		var stats types.DeveloperStatsByEpoch
		bz := epochStore.Get(key)
		if bz != nil {
			k.cdc.MustUnmarshal(bz, &stats)
		} else {
			stats = types.DeveloperStatsByEpoch{
				EpochId:    epochID,
				Inferences: make(map[string]*types.InferenceStats),
			}
		}

		stats.Inferences[inferenceID] = &types.InferenceStats{
			Status:       inferenceStatus,
			AiTokensUsed: tokens,
		}

		epochStore.Set(key, k.cdc.MustMarshal(&stats))
		return nil
	}

	// === CASE 2: inference already exists, but was tagged by different epoch ===
	oldKey := developerByEpochKey(developerAddr, previouslyKnownEpochId)
	bz := epochStore.Get(oldKey)
	if bz == nil {
		return ErrStatsByEpochNotFound
	}

	if previouslyKnownEpochId != epochID {
		var oldStats types.DeveloperStatsByEpoch
		k.cdc.MustUnmarshal(bz, &oldStats)
		delete(oldStats.Inferences, inferenceID)
		epochStore.Set(oldKey, k.cdc.MustMarshal(&oldStats))
	}

	// add inference to new epoch
	newKey := developerByEpochKey(developerAddr, epochID)
	var newStats types.DeveloperStatsByEpoch
	bz = epochStore.Get(newKey)
	if bz != nil {
		k.cdc.MustUnmarshal(bz, &newStats)
	} else {
		newStats = types.DeveloperStatsByEpoch{
			EpochId:    epochID,
			Inferences: make(map[string]*types.InferenceStats),
		}
	}

	newStats.Inferences[inferenceID] = &types.InferenceStats{Status: inferenceStatus, AiTokensUsed: tokens}
	epochStore.Set(newKey, k.cdc.MustMarshal(&newStats))
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

	iter := epochStore.ReverseIterator(nil, nil)
	defer iter.Close()

	var seenEpochs = make(map[uint64]bool)

	for ; iter.Valid(); iter.Next() {
		epochID := sdk.BigEndianToUint64(iter.Key()[:8])
		seenEpochs[epochID] = true

		var stats types.DeveloperStatsByEpoch
		k.cdc.MustUnmarshal(iter.Value(), &stats)

		for _, inf := range stats.Inferences {
			tokensTotal += int64(inf.AiTokensUsed)
			inferenceCount++
		}

		if len(seenEpochs) == n {
			break
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

	iterator := epochStore.ReverseIterator(nil, nil)
	defer iterator.Close()

	var (
		tokensTotal    int64
		inferenceCount int
	)

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

func developerByEpochKey(developerAddr string, epochID uint64) []byte {
	return append(sdk.Uint64ToBigEndian(epochID), []byte(developerAddr)...)
}

func developerByTimeKey(developerAddr string, timestamp uint64) []byte {
	return append(sdk.Uint64ToBigEndian(timestamp), []byte(developerAddr)...)
}
