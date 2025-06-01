package keeper

import (
	"context"
	"cosmossdk.io/store/prefix"
	"encoding/binary"
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

var ErrInferenceAlreadyExistsInOtherEpoch = errors.New("inference exists in other epoch")

func (k Keeper) DevelopersStatsSet(ctx context.Context, developerAddr string, inferenceID string, inferenceStatus types.InferenceStatus, epochID uint64, tokens uint64) error {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	epochStore := prefix.NewStore(storeAdapter, types.KeyPrefix(DevelopersByEpoch))
	epochKey := developerByEpochKey(developerAddr, epochID)

	var statsByEpoch types.DeveloperStatsByEpoch
	bz := epochStore.Get(epochKey)
	if bz != nil {
		k.cdc.MustUnmarshal(bz, &statsByEpoch)
		if statsByEpoch.EpochId != epochID {
			return ErrInferenceAlreadyExistsInOtherEpoch
		}
	} else {
		statsByEpoch = types.DeveloperStatsByEpoch{
			EpochId:    epochID,
			Inferences: make(map[string]*types.InferenceStats),
		}
	}

	inferenceStats := &types.InferenceStats{
		Status:       inferenceStatus,
		AiTokensUsed: tokens,
	}

	statsByEpoch.Inferences[inferenceID] = inferenceStats
	epochStore.Set(epochKey, k.cdc.MustMarshal(&statsByEpoch))

	inferenceTime := time.Now().UTC().Truncate(time.Second).Unix()

	timeStore := prefix.NewStore(storeAdapter, types.KeyPrefix(DevelopersByTime))
	indexStore := prefix.NewStore(storeAdapter, types.KeyPrefix(DevelopersByInference))
	primaryKey := indexStore.Get([]byte(inferenceID))
	if primaryKey == nil {
		timeKey := developerByTimeKey(developerAddr, uint64(inferenceTime))
		timeStore.Set(timeKey, k.cdc.MustMarshal(&types.DeveloperStatsByTime{
			EpochId:   epochID,
			Timestamp: inferenceTime,
			Inference: inferenceStats,
		}))
		indexStore.Set([]byte(inferenceID), timeKey)
		return nil
	}

	var statsByTime types.DeveloperStatsByTime
	if val := timeStore.Get(primaryKey); val != nil {
		k.cdc.MustUnmarshal(val, &statsByTime)
		if statsByTime.EpochId != epochID {
			return ErrInferenceAlreadyExistsInOtherEpoch
		}
	} else {
		statsByTime = types.DeveloperStatsByTime{
			EpochId:   epochID,
			Timestamp: inferenceTime,
		}
	}
	statsByTime.Inference = inferenceStats

	timeKey := developerByTimeKey(developerAddr, uint64(inferenceTime))
	timeStore.Set(timeKey, k.cdc.MustMarshal(&statsByTime))
	indexStore.Set([]byte(inferenceID), timeKey)
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
) []types.DeveloperStatsByTime {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	timeStore := prefix.NewStore(store, types.KeyPrefix(DevelopersByTime))

	var results []types.DeveloperStatsByTime

	startKey := developerByTimeKey(developerAddr, uint64(timeFrom))
	endKey := developerByTimeKey(developerAddr, uint64(timeTo+1))

	iterator := timeStore.Iterator(startKey, endKey)
	defer iterator.Close()

	for ; iterator.Valid(); iterator.Next() {
		var stats types.DeveloperStatsByTime
		k.cdc.MustUnmarshal(iterator.Value(), &stats)
		results = append(results, stats)
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

func (k Keeper) CountTotalInferenceInLastNEpochs(ctx context.Context, currentEpoch uint64, n int) (int64, int) {
	if n <= 0 || currentEpoch <= 1 {
		return 0, 0
	}

	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	epochStore := prefix.NewStore(store, types.KeyPrefix(DevelopersByEpoch))

	startEpoch := currentEpoch - uint64(n)
	start := sdk.Uint64ToBigEndian(startEpoch)
	end := sdk.Uint64ToBigEndian(currentEpoch)

	iterator := epochStore.Iterator(start, end)
	defer iterator.Close()

	var (
		tokensTotal    int64
		inferenceCount int
	)

	for ; iterator.Valid(); iterator.Next() {
		var stats types.DeveloperStatsByEpoch
		k.cdc.MustUnmarshal(iterator.Value(), &stats)

		for _, inf := range stats.Inferences {
			tokensTotal += int64(inf.AiTokensUsed)
			inferenceCount++
		}
	}
	return tokensTotal, inferenceCount
}

func (k Keeper) CountTotalInferenceInLastNEpochsByDeveloper(ctx context.Context, developerAddr string, currentEpoch uint64, n int) (int64, int) {
	if n <= 0 || currentEpoch <= 1 {
		return 0, 0
	}

	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	epochStore := prefix.NewStore(store, types.KeyPrefix(DevelopersByEpoch))

	fromEpoch := currentEpoch - uint64(n)

	iterator := epochStore.Iterator(nil, nil)
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

		epochID := binary.BigEndian.Uint64(key[:8])
		keyDeveloper := string(key[8:])

		if keyDeveloper != developerAddr {
			continue
		}

		if epochID < fromEpoch || epochID >= currentEpoch {
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
