package keeper

import (
	"context"
	"cosmossdk.io/store/prefix"
	storetypes "cosmossdk.io/store/types"
	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/productscience/inference/x/inference/types"
)

// SetEpochPerformanceSummary set a specific epochPerformanceSummary in the store from its index
func (k Keeper) SetEpochPerformanceSummary(ctx context.Context, epochPerformanceSummary types.EpochPerformanceSummary) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, types.KeyPrefix(types.EpochPerformanceSummaryKeyPrefix))
	b := k.cdc.MustMarshal(&epochPerformanceSummary)

	store.Set(types.EpochPerformanceSummaryKey(
		epochPerformanceSummary.ParticipantId,
		epochPerformanceSummary.EpochStartHeight,
	), b)
}

// GetEpochPerformanceSummary returns a epochPerformanceSummary from its index
func (k Keeper) GetEpochPerformanceSummary(
	ctx context.Context,
	epochStartHeight uint64,
	participantId string,

) (val types.EpochPerformanceSummary, found bool) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, types.KeyPrefix(types.EpochPerformanceSummaryKeyPrefix))

	b := store.Get(types.EpochPerformanceSummaryKey(
		participantId,
		epochStartHeight,
	))
	if b == nil {
		return val, false
	}

	k.cdc.MustUnmarshal(b, &val)
	return val, true
}

// RemoveEpochPerformanceSummary removes a epochPerformanceSummary from the store
func (k Keeper) RemoveEpochPerformanceSummary(
	ctx context.Context,
	epochStartHeight uint64,
	participantId string,

) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, types.KeyPrefix(types.EpochPerformanceSummaryKeyPrefix))
	store.Delete(types.EpochPerformanceSummaryKey(
		participantId,
		epochStartHeight,
	))
}

// GetAllEpochPerformanceSummary returns all epochPerformanceSummary
func (k Keeper) GetAllEpochPerformanceSummary(ctx context.Context) (list []types.EpochPerformanceSummary) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, types.KeyPrefix(types.EpochPerformanceSummaryKeyPrefix))
	iterator := storetypes.KVStorePrefixIterator(store, []byte{})

	defer iterator.Close()

	for ; iterator.Valid(); iterator.Next() {
		var val types.EpochPerformanceSummary
		k.cdc.MustUnmarshal(iterator.Value(), &val)
		list = append(list, val)
	}

	return
}

// GetEpochPerformanceSummariesByParticipant returns all epochPerformanceSummary for a specific participant
func (k Keeper) GetEpochPerformanceSummariesByParticipant(ctx context.Context, participantId string) (list []types.EpochPerformanceSummary) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, types.KeyPrefix(types.EpochPerformanceSummaryKeyPrefix))
	iterator := storetypes.KVStorePrefixIterator(store, types.EpochPerformanceSummaryKeyParticipantPrefix(participantId))

	defer iterator.Close()

	for ; iterator.Valid(); iterator.Next() {
		var val types.EpochPerformanceSummary
		k.cdc.MustUnmarshal(iterator.Value(), &val)
		list = append(list, val)
	}

	return
}

func (k Keeper) GetParticipantsEpochSummaries(
	ctx context.Context,
	participantIds []string,
	epochStartHeight uint64,
) []types.EpochPerformanceSummary {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, types.KeyPrefix(types.EpochPerformanceSummaryKeyPrefix))

	var summaries []types.EpochPerformanceSummary
	for _, participantId := range participantIds {
		key := types.EpochPerformanceSummaryKey(participantId, epochStartHeight)
		bz := store.Get(key)
		if bz == nil {
			continue
		}

		var summary types.EpochPerformanceSummary
		k.cdc.MustUnmarshal(bz, &summary)
		summaries = append(summaries, summary)
	}
	return summaries
}
