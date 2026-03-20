package keeper

import (
	"context"

	"github.com/cosmos/cosmos-sdk/codec"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

type epochDataTransientParticipantCache struct {
	Weight     int64
	Reputation int32
}

type epochDataTransientModelMetaCacheEntry struct {
	EpochPolicy         string
	TotalWeight         int64
	ValidationThreshold *types.Decimal
	SubGroupModels      []string
}

func (k Keeper) BuildEpochDataTransientCache(ctx context.Context) error {
	currentEpochIndex, found := k.GetEffectiveEpochIndex(ctx)
	if !found {
		return nil
	}

	for _, epochIndex := range cachedEpochDataEpochs(currentEpochIndex) {
		rootGroupData, found := k.GetEpochGroupData(ctx, epochIndex, "")
		if !found {
			continue
		}
		transientStore := k.transientStoreService.OpenTransientStore(ctx)
		if err := setModelTransientCacheEntries(transientStore, k.cdc, epochIndex, "", rootGroupData); err != nil {
			return err
		}

		for _, modelID := range rootGroupData.SubGroupModels {
			modelGroupData, found := k.GetEpochGroupData(ctx, epochIndex, modelID)
			if !found {
				continue
			}
			if err := setModelTransientCacheEntries(transientStore, k.cdc, epochIndex, modelID, modelGroupData); err != nil {
				return err
			}
		}
	}

	return nil
}

func (k Keeper) GetCachedEpochDataModelMeta(
	ctx context.Context,
	epochIndex uint64,
	modelID string,
) (epochDataTransientModelMetaCacheEntry, bool, error) {
	transientStore := k.transientStoreService.OpenTransientStore(ctx)

	bz, err := transientStore.Get(epochDataModelMetaCacheKey(epochIndex, modelID))
	if err != nil || len(bz) == 0 {
		return epochDataTransientModelMetaCacheEntry{}, false, err
	}

	var cachedGroupData types.EpochGroupData
	if err := k.cdc.Unmarshal(bz, &cachedGroupData); err != nil {
		return epochDataTransientModelMetaCacheEntry{}, false, err
	}
	entry := epochDataTransientModelMetaCacheEntry{
		EpochPolicy:    cachedGroupData.EpochPolicy,
		TotalWeight:    cachedGroupData.TotalWeight,
		SubGroupModels: cachedGroupData.SubGroupModels,
	}
	if cachedGroupData.ModelSnapshot != nil {
		entry.ValidationThreshold = cachedGroupData.ModelSnapshot.ValidationThreshold
	}
	return entry, true, nil
}

func (k Keeper) GetCachedEpochDataModelWeight(
	ctx context.Context,
	epochIndex uint64,
	modelID string,
	validator string,
) (epochDataTransientParticipantCache, bool, error) {
	transientStore := k.transientStoreService.OpenTransientStore(ctx)

	bz, err := transientStore.Get(epochDataModelWeightCacheKey(epochIndex, modelID, validator))
	if err != nil || len(bz) == 0 {
		return epochDataTransientParticipantCache{}, false, err
	}

	var cachedWeight types.ValidationWeight
	if err := k.cdc.Unmarshal(bz, &cachedWeight); err != nil {
		return epochDataTransientParticipantCache{}, false, err
	}
	entry := epochDataTransientParticipantCache{
		Weight:     cachedWeight.Weight,
		Reputation: cachedWeight.Reputation,
	}
	return entry, true, nil
}

func epochDataModelMetaCacheKey(epochIndex uint64, modelID string) []byte {
	return appendEpochDataTransientCacheKey(
		types.TransientEpochDataModelMetaKey,
		epochIndex,
		modelID,
	)
}

func epochDataModelWeightCacheKey(epochIndex uint64, modelID, validator string) []byte {
	return appendEpochDataTransientCacheKey(
		types.TransientEpochDataModelWeightKey,
		epochIndex,
		modelID,
		validator,
	)
}

func appendEpochDataTransientCacheKey(prefix []byte, epochIndex uint64, parts ...string) []byte {
	key := append([]byte{}, prefix...)
	key = append(key, sdk.Uint64ToBigEndian(epochIndex)...)
	for _, part := range parts {
		key = sdk.AppendLengthPrefixedBytes(key, []byte(part))
	}
	return key
}

func setModelTransientCacheEntries(
	transientStore transientStoreSetter,
	cdc codec.BinaryCodec,
	epochIndex uint64,
	modelID string,
	groupData types.EpochGroupData,
) error {
	metaGroupData := types.EpochGroupData{
		EpochPolicy:    groupData.EpochPolicy,
		TotalWeight:    groupData.TotalWeight,
		SubGroupModels: groupData.SubGroupModels,
	}
	if groupData.ModelSnapshot != nil {
		metaGroupData.ModelSnapshot = &types.Model{
			ValidationThreshold: groupData.ModelSnapshot.ValidationThreshold,
		}
	}
	metaBz, err := cdc.Marshal(&metaGroupData)
	if err != nil {
		return err
	}
	if err := transientStore.Set(epochDataModelMetaCacheKey(epochIndex, modelID), metaBz); err != nil {
		return err
	}

	for _, weight := range groupData.ValidationWeights {
		if weight == nil {
			continue
		}
		validationEntry := types.ValidationWeight{
			Weight:     weight.Weight,
			Reputation: weight.Reputation,
		}
		validationBz, err := cdc.Marshal(&validationEntry)
		if err != nil {
			return err
		}
		if err := transientStore.Set(epochDataModelWeightCacheKey(epochIndex, modelID, weight.MemberAddress), validationBz); err != nil {
			return err
		}
	}

	return nil
}

type transientStoreSetter interface {
	Set(key, value []byte) error
}

func cachedEpochDataEpochs(currentEpochIndex uint64) []uint64 {
	if currentEpochIndex == 0 {
		return []uint64{0}
	}
	return []uint64{currentEpochIndex, currentEpochIndex - 1}
}
