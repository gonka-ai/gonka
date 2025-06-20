package keeper

import (
	"bytes"
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

func (k Keeper) SetDeveloperStats(ctx context.Context, inference types.Inference) error {
	k.LogInfo("SetDeveloperStats: got stat", types.Stat, "inference_id", inference.InferenceId, "inference_status", inference.Status.String(), "developer", inference.RequestedBy, "poc_block_height", inference.EpochGroupId)
	epoch, err := k.GetCurrentEpochGroup(ctx)
	if err != nil {
		return err
	}

	currentEpochId := epoch.GroupData.EpochGroupId
	tokens := inference.CompletionTokenCount + inference.PromptTokenCount
	inferenceTime := inference.StartBlockTimestamp
	if inferenceTime == 0 {
		inferenceTime = inference.EndBlockTimestamp
	}

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
		k.LogInfo("completely new record, create record by time", types.Stat, "inference_id", inference.InferenceId, "developer", inference.RequestedBy)
		timeKey = developerByTimeAndInferenceKey(inference.RequestedBy, uint64(inferenceTime), inference.InferenceId)
		byTimeStore.Set(timeKey, k.cdc.MustMarshal(&types.DeveloperStatsByTime{
			EpochId:   currentEpochId,
			Timestamp: inferenceTime,
			Inference: inferenceStats,
		}))
		byInferenceStore.Set([]byte(inference.InferenceId), timeKey)
		modelKey := modelByTimeKey(inference.Model, inferenceTime, inference.InferenceId)
		byModelsStore.Set(modelKey, timeKey)
		return k.setStatByEpoch(ctx, inference, currentEpochId, 0)
	}

	var (
		statsByTime types.DeveloperStatsByTime
		prevEpochId uint64
	)
	if val := byTimeStore.Get(timeKey); val != nil {
		k.LogInfo("record found by time key", types.Stat, "inference_id", inference.InferenceId, "developer", inference.RequestedBy)
		k.cdc.MustUnmarshal(val, &statsByTime)
		prevEpochId = statsByTime.EpochId

		statsByTime.EpochId = currentEpochId
		statsByTime.Inference.Status = inference.Status
		statsByTime.Inference.AiTokensUsed = tokens
		statsByTime.Inference.EpochPocBlockHeight = inference.EpochGroupId
		statsByTime.Inference.ActualConstInCoins = inference.ActualCost
	} else {
		k.LogInfo("time key exists, record DO NOT exist", types.Stat, "inference_id", inference.InferenceId, "developer", inference.RequestedBy)
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
	return k.setStatByEpoch(ctx, inference, currentEpochId, prevEpochId)
}

func (k Keeper) setStatByEpoch(
	ctx context.Context,
	inference types.Inference,
	currentEpochId uint64,
	previouslyKnownEpochId uint64,
) error {
	k.LogInfo("stat set by epoch", types.Stat, "inference_id", inference.InferenceId, "developer", inference.RequestedBy, "epoch_id", currentEpochId, "previously_known_epoch_id", previouslyKnownEpochId)
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	epochStore := prefix.NewStore(storeAdapter, types.KeyPrefix(DevelopersByEpoch))

	// === CASE 1: inference already exists, but was tagged by different epoch ===
	if previouslyKnownEpochId != 0 && previouslyKnownEpochId != currentEpochId {
		k.LogInfo("stat set by epoch: inference already exists, but was tagged by different epoch, clean up", types.Stat, "inference_id", inference.InferenceId, "developer", inference.RequestedBy, "epoch_id", currentEpochId)
		oldKey := developerByEpochKey(inference.RequestedBy, previouslyKnownEpochId)
		if bz := epochStore.Get(oldKey); bz != nil {
			var oldStats types.DeveloperStatsByEpoch
			k.cdc.MustUnmarshal(bz, &oldStats)

			oldStats.InferenceIds = removeInferenceId(oldStats.InferenceIds, inference.InferenceId)
			epochStore.Set(oldKey, k.cdc.MustMarshal(&oldStats))
		}
	}

	// === CASE 2: create new record or update existing with current_epoch_id ===
	k.LogInfo("stat set by epoch: new record or same epoch", types.Stat, "inference_id", inference.InferenceId, "developer", inference.RequestedBy, "epoch_id", currentEpochId)
	newKey := developerByEpochKey(inference.RequestedBy, currentEpochId)
	var newStats types.DeveloperStatsByEpoch
	if bz := epochStore.Get(newKey); bz != nil {
		k.cdc.MustUnmarshal(bz, &newStats)
		if newStats.InferenceIds == nil {
			newStats.InferenceIds = make([]string, 0)
		}
	} else {
		newStats = types.DeveloperStatsByEpoch{
			EpochId:      currentEpochId,
			InferenceIds: make([]string, 0),
		}
	}

	newStats.InferenceIds = append(newStats.InferenceIds, inference.InferenceId)
	epochStore.Set(newKey, k.cdc.MustMarshal(&newStats))
	k.LogInfo("stat set by epoch: inference successfully added to epoch", types.Stat, "inference_id", inference.InferenceId, "developer", inference.RequestedBy, "epoch_id", currentEpochId)
	return nil
}

func (k Keeper) GetDevelopersStatsByEpoch(ctx context.Context, developerAddr string, epochId uint64) (types.DeveloperStatsByEpoch, bool) {
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

func (k Keeper) GetDeveloperStatsByTime(
	ctx context.Context,
	developerAddr string,
	timeFrom, timeTo int64,
) []*types.DeveloperStatsByTime {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	timeStore := prefix.NewStore(store, types.KeyPrefix(DevelopersByTime))

	var results []*types.DeveloperStatsByTime

	startKey := developerByTimeAndInferenceKey(developerAddr, uint64(timeFrom), "")
	endKey := developerByTimeAndInferenceKey(developerAddr, uint64(timeTo+1), "")

	iterator := timeStore.Iterator(startKey, endKey)
	defer iterator.Close()

	for ; iterator.Valid(); iterator.Next() {
		if addr := extractDeveloperAddrFromKey(iterator.Key()); addr != developerAddr {
			continue
		}

		var stats types.DeveloperStatsByTime
		k.cdc.MustUnmarshal(iterator.Value(), &stats)
		results = append(results, &stats)
	}
	return results
}

func (k Keeper) GetSummaryByTime(ctx context.Context, from, to int64) StatsSummary {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	timeStore := prefix.NewStore(store, types.KeyPrefix(DevelopersByTime))

	start := sdk.Uint64ToBigEndian(uint64(from))
	end := sdk.Uint64ToBigEndian(uint64(to + 1))

	iterator := timeStore.Iterator(start, end)
	defer iterator.Close()

	summary := StatsSummary{}
	for ; iterator.Valid(); iterator.Next() {
		// covers corner case when we have inferences with empty requestedBy filed, because
		// dev had insufficient funds for payment-on-escrow
		if addr := extractDeveloperAddrFromKey(iterator.Key()); addr == "" {
			continue
		}

		var stats types.DeveloperStatsByTime
		k.cdc.MustUnmarshal(iterator.Value(), &stats)
		summary.TokensUsed += int64(stats.Inference.AiTokensUsed)
		summary.InferenceCount++
		summary.ActualCost += stats.Inference.ActualConstInCoins
	}
	return summary
}

func (k Keeper) GetSummaryLastNEpochs(ctx context.Context, n int) StatsSummary {
	if n <= 0 {
		return StatsSummary{}
	}

	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	epochStore := prefix.NewStore(store, types.KeyPrefix(DevelopersByEpoch))
	byInferenceStore := prefix.NewStore(store, types.KeyPrefix(DevelopersByInference))
	byTimeStore := prefix.NewStore(store, types.KeyPrefix(DevelopersByTime))

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
		// covers corner case when we have inferences with empty requestedBy filed, because
		// dev had insufficient funds for payment-on-escrow
		if addr := extractDeveloperAddrFromKey(iter.Key()); addr == "" {
			continue
		}

		var stats types.DeveloperStatsByEpoch
		k.cdc.MustUnmarshal(iter.Value(), &stats)
		for _, infId := range stats.InferenceIds {
			timeKey := byInferenceStore.Get([]byte(infId))
			if timeKey == nil {
				k.LogError("inconsistent statistic: statistic by epoch has inference id, which doesn't have time key", types.Stat, "inference", infId)
				continue
			}

			var statsByTime types.DeveloperStatsByTime
			if val := byTimeStore.Get(timeKey); val != nil {
				k.cdc.MustUnmarshal(val, &statsByTime)
				summary.TokensUsed += int64(statsByTime.Inference.AiTokensUsed)
				summary.InferenceCount++
				summary.ActualCost += statsByTime.Inference.ActualConstInCoins
			} else {
				k.LogError("inconsistent statistic: time key exists without inference object", types.Stat, "inference", infId)
				continue
			}
		}
	}
	return summary
}

func (k Keeper) GetSummaryLastNEpochsByDeveloper(ctx context.Context, developerAddr string, n int) StatsSummary {
	if n <= 0 {
		return StatsSummary{}
	}

	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	epochStore := prefix.NewStore(store, types.KeyPrefix(DevelopersByEpoch))
	byInferenceStore := prefix.NewStore(store, types.KeyPrefix(DevelopersByInference))
	byTimeStore := prefix.NewStore(store, types.KeyPrefix(DevelopersByTime))

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
		if addr := extractDeveloperAddrFromKey(iterator.Key()); addr != developerAddr {
			continue
		}

		var stats types.DeveloperStatsByEpoch
		k.cdc.MustUnmarshal(iterator.Value(), &stats)
		for _, infId := range stats.InferenceIds {
			timeKey := byInferenceStore.Get([]byte(infId))
			if timeKey == nil {
				k.LogError("inconsistent statistic: statistic by epoch has inference id, which doesn't have time key", types.Stat, "inference", infId)
				continue
			}

			var statsByTime types.DeveloperStatsByTime
			if val := byTimeStore.Get(timeKey); val != nil {
				k.cdc.MustUnmarshal(val, &statsByTime)
				summary.TokensUsed += int64(statsByTime.Inference.AiTokensUsed)
				summary.InferenceCount++
				summary.ActualCost += statsByTime.Inference.ActualConstInCoins
			} else {
				k.LogError("inconsistent statistic: time key exists without inference object", types.Stat, "inference", infId)
				continue
			}
		}
	}
	return summary
}

func (k Keeper) GetSummaryByModelAndTime(ctx context.Context, from, to int64) map[string]StatsSummary {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	timeStore := prefix.NewStore(store, types.KeyPrefix(DevelopersByTime))

	start := sdk.Uint64ToBigEndian(uint64(from))
	end := sdk.Uint64ToBigEndian(uint64(to + 1))

	iter := timeStore.Iterator(start, end)
	defer iter.Close()

	stats := make(map[string]StatsSummary)

	for ; iter.Valid(); iter.Next() {
		// covers corner case when we have inferences with empty requestedBy filed, because
		// dev had insufficient funds for payment-on-escrow
		if addr := extractDeveloperAddrFromKey(iter.Key()); addr == "" {
			continue
		}

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

func (k Keeper) DumpAllDeveloperStats(ctx context.Context) (map[string][]*types.DeveloperStatsByEpoch, map[string][]*types.DeveloperStatsByTime) {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))

	// === DeveloperStatsByEpoch ===
	epochStore := prefix.NewStore(store, types.KeyPrefix(DevelopersByEpoch))
	epochIter := epochStore.Iterator(nil, nil)
	defer epochIter.Close()

	epochStats := make(map[string][]*types.DeveloperStatsByEpoch)
	for ; epochIter.Valid(); epochIter.Next() {
		var stats types.DeveloperStatsByEpoch
		k.cdc.MustUnmarshal(epochIter.Value(), &stats)

		developer := extractDeveloperAddrFromKey(epochIter.Key())
		stat := epochStats[developer]
		if stat == nil {
			stat = make([]*types.DeveloperStatsByEpoch, 0)
		}
		stat = append(stat, &stats)
		epochStats[developer] = stat
	}

	// === DeveloperStatsByTime ===
	timeStore := prefix.NewStore(store, types.KeyPrefix(DevelopersByTime))
	timeIter := timeStore.Iterator(nil, nil)
	defer timeIter.Close()

	timeStats := make(map[string][]*types.DeveloperStatsByTime)
	for ; timeIter.Valid(); timeIter.Next() {
		var stats types.DeveloperStatsByTime
		k.cdc.MustUnmarshal(timeIter.Value(), &stats)

		developer := extractDeveloperAddrFromKey(timeIter.Key())
		stat := timeStats[developer]
		if stat == nil {
			stat = make([]*types.DeveloperStatsByTime, 0)
		}
		stat = append(stat, &stats)
		timeStats[developer] = stat
	}
	return epochStats, timeStats
}

func modelByTimeKey(model string, timestamp int64, inferenceId string) []byte {
	modelKey := append([]byte(model+"|"), sdk.Uint64ToBigEndian(uint64(timestamp))...)
	return append(modelKey, []byte(inferenceId)...)
}

var keySeparator = []byte("__SEP__")

func developerByEpochKey(developerAddr string, epochId uint64) []byte {
	return append(append(sdk.Uint64ToBigEndian(epochId), keySeparator...), []byte(developerAddr)...)
}

func developerByTimeAndInferenceKey(developerAddr string, timestamp uint64, inferenceId string) []byte {
	key := developerByTimeKey(developerAddr, timestamp)
	key = append(key, keySeparator...)
	key = append(key, []byte(inferenceId)...)
	return key
}

func developerByTimeKey(developerAddr string, timestamp uint64) []byte {
	key := append(sdk.Uint64ToBigEndian(timestamp), keySeparator...)
	key = append(key, []byte(developerAddr)...)
	return key
}

func extractDeveloperAddrFromKey(key []byte) string {
	parts := bytes.Split(key, keySeparator)
	if len(parts) < 2 {
		return ""
	}
	return string(parts[1])
}

func removeInferenceId(slice []string, inferenceId string) []string {
	for i, v := range slice {
		if v == inferenceId {
			return append(slice[:i], slice[i+1:]...)
		}
	}
	return slice
}
