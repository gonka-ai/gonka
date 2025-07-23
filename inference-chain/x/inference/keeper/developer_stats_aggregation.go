package keeper

import (
	"bytes"
	"context"

	"cosmossdk.io/store/prefix"
	"github.com/cosmos/cosmos-sdk/runtime"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

type StatsSummary struct {
	InferenceCount int
	TokensUsed     int64
	ActualCost     int64
}

func (k Keeper) SetDeveloperStats(ctx context.Context, inference types.Inference) error {
	epochId := inference.EpochId
	if epochId == 0 {
		effectiveEpoch, found := k.GetEffectiveEpoch(ctx)
		if !found {
			k.LogError("SetDeveloperStats. failed to get effective epoch index for inference", types.Stat, "inference_id", inference.InferenceId)
			return types.ErrEffectiveEpochNotFound.Wrapf("SetDeveloperStats. failed to get effective epoch index for inference %s", inference.InferenceId)
		}
		epochId = effectiveEpoch.Index
	}

	k.LogInfo("SetDeveloperStats: got stat", types.Stat,
		"inference_id", inference.InferenceId,
		"inference_status", inference.Status.String(),
		"developer", inference.RequestedBy,
		"poc_block_height", inference.EpochPocStartBlockHeight,
		"epoch_id", epochId)

	tokens := inference.CompletionTokenCount + inference.PromptTokenCount
	inferenceTime := inference.EndBlockTimestamp
	if inferenceTime == 0 {
		inferenceTime = inference.StartBlockTimestamp
	}

	inferenceStats := types.InferenceStats{
		EpochId:           inference.EpochId,
		InferenceId:       inference.InferenceId,
		Status:            inference.Status,
		TotalTokenCount:   tokens,
		Model:             inference.Model,
		ActualCostInCoins: inference.ActualCost,
	}

	inferencePrevEpochId, err := k.setOrUpdateInferenceStatByTime(ctx, inference.RequestedBy, inferenceStats, inferenceTime, epochId)
	if err != nil {
		return err
	}
	k.setInferenceStatsByModel(ctx, inference.RequestedBy, inferenceStats, inferenceTime)
	k.setOrUpdateInferenceStatsByEpoch(ctx, inference.RequestedBy, inferenceStats, epochId, inferencePrevEpochId)
	return nil
}

// TODO: refactor it later (move ”getter' logic to store level)
func (k Keeper) GetDevelopersStatsByEpoch(ctx context.Context, developerAddr string, epochId uint64) (types.DeveloperStatsByEpoch, bool) {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	epochStore := prefix.NewStore(store, types.KeyPrefix(StatsDevelopersByEpoch))
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
	timeStore := prefix.NewStore(store, types.KeyPrefix(StatsDevelopersByTime))

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
	timeStore := prefix.NewStore(store, types.KeyPrefix(StatsDevelopersByTime))

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
		summary.TokensUsed += int64(stats.Inference.TotalTokenCount)
		summary.InferenceCount++
		summary.ActualCost += stats.Inference.ActualCostInCoins
	}
	return summary
}

func (k Keeper) GetSummaryLastNEpochs(ctx context.Context, n int) StatsSummary {
	if n <= 0 {
		return StatsSummary{}
	}

	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	epochStore := prefix.NewStore(store, types.KeyPrefix(StatsDevelopersByEpoch))
	byInferenceStore := prefix.NewStore(store, types.KeyPrefix(StatsDevelopersByInference))
	byTimeStore := prefix.NewStore(store, types.KeyPrefix(StatsDevelopersByTime))

	effectiveEpochIndex, found := k.GetEffectiveEpochIndex(ctx)
	k.LogInfo("GetSummaryLastNEpochs: fetched effectiveEpochIndex", types.Stat, "effectiveEpochIndex", effectiveEpochIndex)
	if !found {
		k.LogError("GetSummaryLastNEpochs. failed to get effective epoch index.", types.Stat)
		return StatsSummary{}
	}
	epochIdFrom := effectiveEpochIndex - uint64(n)
	epochIdTo := effectiveEpochIndex

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
				summary.TokensUsed += int64(statsByTime.Inference.TotalTokenCount)
				summary.InferenceCount++
				summary.ActualCost += statsByTime.Inference.ActualCostInCoins
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
	epochStore := prefix.NewStore(store, types.KeyPrefix(StatsDevelopersByEpoch))
	byInferenceStore := prefix.NewStore(store, types.KeyPrefix(StatsDevelopersByInference))
	byTimeStore := prefix.NewStore(store, types.KeyPrefix(StatsDevelopersByTime))

	effectiveEpochIndex, found := k.GetEffectiveEpochIndex(ctx)
	if !found {
		k.LogError("GetSummaryLastNEpochsByDeveloper. failed to get effective epoch index.", types.Stat, "developerAddr", developerAddr)
		return StatsSummary{}
	}
	epochIdFrom := effectiveEpochIndex - uint64(n)
	epochIdTo := effectiveEpochIndex

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
				summary.TokensUsed += int64(statsByTime.Inference.TotalTokenCount)
				summary.InferenceCount++
				summary.ActualCost += statsByTime.Inference.ActualCostInCoins
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
	timeStore := prefix.NewStore(store, types.KeyPrefix(StatsDevelopersByTime))

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
		s.TokensUsed += int64(stat.Inference.TotalTokenCount)
		s.ActualCost += stat.Inference.ActualCostInCoins
		stats[model] = s
	}
	return stats
}

func (k Keeper) DumpAllDeveloperStats(ctx context.Context) (map[string][]*types.DeveloperStatsByEpoch, map[string][]*types.DeveloperStatsByTime) {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))

	// === DeveloperStatsByEpoch ===
	epochStore := prefix.NewStore(store, types.KeyPrefix(StatsDevelopersByEpoch))
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
	timeStore := prefix.NewStore(store, types.KeyPrefix(StatsDevelopersByTime))
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

func inferenceIdExists(slice []string, inferenceId string) bool {
	for _, v := range slice {
		if v == inferenceId {
			return true
		}
	}
	return false
}
