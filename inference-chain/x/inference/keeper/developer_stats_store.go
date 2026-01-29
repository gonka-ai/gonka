package keeper

import (
	"context"

	"cosmossdk.io/store/prefix"
	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/productscience/inference/x/inference/types"
)

const (
	StatsDevelopersByEpoch             = "stats/developers/epoch"
	StatsDevelopersByTime              = "stats/developers/time"
	StatsDevelopersByInference         = "stats/developers/inference"
	StatsDevelopersByInferenceAndModel = "stats/model/inference"
)

func (k Keeper) setOrUpdateInferenceStatByTime(ctx context.Context, developer string, infStats types.InferenceStats, inferenceTime int64, epochId uint64) (uint64, error) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	byInferenceStore := prefix.NewStore(storeAdapter, types.KeyPrefix(StatsDevelopersByInference))
	byTimeStore := prefix.NewStore(storeAdapter, types.KeyPrefix(StatsDevelopersByTime))

	timeKey := byInferenceStore.Get([]byte(infStats.InferenceId))
	if timeKey == nil {
		// completely new record
		k.LogInfo("completely new record, create record by time", types.Stat, "inference_id", infStats.InferenceId, "developer", developer)
		timeKey = developerByTimeAndInferenceKey(developer, uint64(inferenceTime), infStats.InferenceId)
		bz, err := k.cdc.Marshal(&types.DeveloperStatsByTime{
			EpochId:   epochId,
			Timestamp: inferenceTime,
			Inference: &infStats,
		})
		if err != nil {
			return 0, err
		}
		byTimeStore.Set(timeKey, bz)
		byInferenceStore.Set([]byte(infStats.InferenceId), timeKey)
		return 0, nil
	}

	var (
		statsByTime types.DeveloperStatsByTime
		prevEpochId uint64
	)

	if val := byTimeStore.Get(timeKey); val != nil {
		k.LogInfo("record found by time key", types.Stat, "inference_id", infStats.InferenceId, "developer", developer)
		err := k.cdc.Unmarshal(val, &statsByTime)
		if err != nil {
			return 0, err
		}
		prevEpochId = statsByTime.EpochId

		prevInferenceTime := statsByTime.Timestamp
		if prevInferenceTime != inferenceTime {
			statsByTime.Timestamp = inferenceTime
			byTimeStore.Delete(timeKey)
			timeKey = developerByTimeAndInferenceKey(developer, uint64(inferenceTime), infStats.InferenceId)
		}

		statsByTime.EpochId = epochId
		statsByTime.Inference.Status = infStats.Status
		statsByTime.Inference.TotalTokenCount = infStats.TotalTokenCount
		statsByTime.Inference.EpochId = infStats.EpochId
		statsByTime.Inference.ActualCostInCoins = infStats.ActualCostInCoins
	} else {
		k.LogInfo("time key exists, record DO NOT exist", types.Stat, "inference_id", infStats.InferenceId, "developer", developer)
		statsByTime = types.DeveloperStatsByTime{
			EpochId:   epochId,
			Timestamp: inferenceTime,
			Inference: &infStats,
		}
	}
	bz, err := k.cdc.Marshal(&statsByTime)
	if err != nil {
		return 0, err
	}
	byTimeStore.Set(timeKey, bz)
	byInferenceStore.Set([]byte(infStats.InferenceId), timeKey)

	return prevEpochId, nil
}

func (k Keeper) setInferenceStatsByModel(ctx context.Context, developer string, stats types.InferenceStats, inferenceTime int64) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	byModelsStore := prefix.NewStore(storeAdapter, types.KeyPrefix(StatsDevelopersByInferenceAndModel))

	modelKey := modelByTimeKey(stats.Model, inferenceTime, stats.InferenceId)
	byModelsStore.Set(modelKey, developerByTimeAndInferenceKey(developer, uint64(inferenceTime), stats.InferenceId))
}

func (k Keeper) setOrUpdateInferenceStatsByEpoch(ctx context.Context, developer string, infStats types.InferenceStats, currentEpochId, prevEpochId uint64) error {
	k.LogDebug("stat set by epoch", types.Stat, "inference_id", infStats.InferenceId, "developer", developer, "epoch_id", currentEpochId, "previously_known_epoch_id", prevEpochId)
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	epochStore := prefix.NewStore(storeAdapter, types.KeyPrefix(StatsDevelopersByEpoch))

	// === CASE 1: inference already exists, but was tagged by different epoch ===
	if prevEpochId != 0 && prevEpochId != currentEpochId {
		k.LogDebug("stat set by epoch: inference already exists, but was tagged by different epoch, clean up", types.Stat, "inference_id", infStats.InferenceId, "developer", developer, "epoch_id", currentEpochId)
		oldKey := developerByEpochKey(developer, prevEpochId)
		if bz := epochStore.Get(oldKey); bz != nil {
			var oldStats types.DeveloperStatsByEpoch
			err := k.cdc.Unmarshal(bz, &oldStats)
			if err != nil {
				return err
			}

			oldStats.InferenceIds = removeInferenceId(oldStats.InferenceIds, infStats.InferenceId)
			bz, err = k.cdc.Marshal(&oldStats)
			if err != nil {
				return err
			}
			epochStore.Set(oldKey, bz)
		}
	}

	// === CASE 2: create new record or update existing with current_epoch_id ===
	k.LogDebug("stat set by epoch: new record or same epoch", types.Stat, "inference_id", infStats.InferenceId, "developer", developer, "epoch_id", currentEpochId)
	newKey := developerByEpochKey(developer, currentEpochId)
	var newStats types.DeveloperStatsByEpoch
	if bz := epochStore.Get(newKey); bz != nil {
		err := k.cdc.Unmarshal(bz, &newStats)
		if err != nil {
			return err
		}
		if newStats.InferenceIds == nil {
			newStats.InferenceIds = make([]string, 0)
		}
	} else {
		newStats = types.DeveloperStatsByEpoch{
			EpochId:      currentEpochId,
			InferenceIds: make([]string, 0),
		}
	}

	if !inferenceIdExists(newStats.InferenceIds, infStats.InferenceId) {
		newStats.InferenceIds = append(newStats.InferenceIds, infStats.InferenceId)
		bz, err := k.cdc.Marshal(&newStats)
		if err != nil {
			return err
		}
		epochStore.Set(newKey, bz)
	}
	k.LogDebug("stat set by epoch: inference successfully added to epoch", types.Stat, "inference_id", infStats.InferenceId, "developer", developer, "epoch_id", currentEpochId)
	return nil
}
