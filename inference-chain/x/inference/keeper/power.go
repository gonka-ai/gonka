package keeper

import (
	"context"
	"cosmossdk.io/store/prefix"
	storetypes "cosmossdk.io/store/types"
	"github.com/cosmos/cosmos-sdk/runtime"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/epochgroup"
	"github.com/productscience/inference/x/inference/types"
)

const (
	EffectiveEpochGroupKey = "effective-epoch-group"
	UpcomingEpochGroupKey  = "upcoming-epoch-group"
	PreviousEpochGroupKey  = "previous-epoch-group"
)

func (k Keeper) SetEffectiveEpochGroupId(ctx context.Context, pocStartHeight uint64) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, types.KeyPrefix(types.EpochGroupPrefix))

	key := []byte(EffectiveEpochGroupKey)
	value := sdk.Uint64ToBigEndian(pocStartHeight)
	store.Set(key, value)
}

func (k Keeper) GetEffectiveEpochGroupId(ctx context.Context) (pocStartHeight uint64) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, types.KeyPrefix(types.EpochGroupPrefix))

	key := []byte(EffectiveEpochGroupKey)
	value := store.Get(key)

	if value == nil {
		return 0
	}

	return sdk.BigEndianToUint64(value)
}

func (k Keeper) SetUpcomingEpochGroupId(ctx context.Context, pocStartHeight uint64) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, types.KeyPrefix(types.EpochGroupPrefix))

	key := []byte(UpcomingEpochGroupKey)
	value := sdk.Uint64ToBigEndian(pocStartHeight)
	store.Set(key, value)
}

func (k Keeper) GetUpcomingEpochGroupId(ctx context.Context) (pocStartHeight uint64) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, types.KeyPrefix(types.EpochGroupPrefix))

	key := []byte(UpcomingEpochGroupKey)
	value := store.Get(key)

	if value == nil {
		return 0
	}

	return sdk.BigEndianToUint64(value)
}

func (k Keeper) SetPreviousEpochGroupId(ctx context.Context, pocStartHeight uint64) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, types.KeyPrefix(types.EpochGroupPrefix))

	key := []byte(PreviousEpochGroupKey)
	value := sdk.Uint64ToBigEndian(pocStartHeight)
	store.Set(key, value)
}

func (k Keeper) GetPreviousEpochGroupId(ctx context.Context) (pocStartHeight uint64) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, types.KeyPrefix(types.EpochGroupPrefix))

	key := []byte(PreviousEpochGroupKey)
	value := store.Get(key)

	if value == nil {
		return 0
	}

	return sdk.BigEndianToUint64(value)
}

func (k Keeper) GetCurrentEpochGroup(ctx context.Context) (*epochgroup.EpochGroup, error) {
	currentId := k.GetEffectiveEpochGroupId(ctx)
	return k.GetEpochGroup(ctx, currentId)
}

func (k Keeper) GetUpcomingEpochGroup(ctx context.Context) (*epochgroup.EpochGroup, error) {
	currentId := k.GetUpcomingEpochGroupId(ctx)
	return k.GetEpochGroup(ctx, currentId)
}

func (k Keeper) GetPreviousEpochGroup(ctx context.Context) (*epochgroup.EpochGroup, error) {
	currentId := k.GetPreviousEpochGroupId(ctx)
	return k.GetEpochGroup(ctx, currentId)
}

func (k Keeper) GetEpochGroup(ctx context.Context, pocStartHeight uint64) (*epochgroup.EpochGroup, error) {
	data, found := k.GetEpochGroupData(ctx, pocStartHeight)
	if !found {
		data = types.EpochGroupData{
			PocStartBlockHeight: pocStartHeight,
		}
		k.SetEpochGroupData(ctx, data)
	}

	return &epochgroup.EpochGroup{
		GroupKeeper:       k.group,
		ParticipantKeeper: k,
		Authority:         k.GetAuthority(),
		Logger:            k,
		GroupDataKeeper:   k,
		GroupData:         &data,
	}, nil
}

func (k Keeper) SetUpcomingPower(ctx context.Context, power types.Power) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, types.KeyPrefix(types.PowerKeyPrefix))
	key := types.PowerKey(power.ParticipantAddress)

	b := k.cdc.MustMarshal(&power)
	store.Set(key, b)
}

func (k Keeper) AllUpcomingPower(ctx context.Context) (list []types.Power) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, types.KeyPrefix(types.PowerKeyPrefix))

	iterator := storetypes.KVStorePrefixIterator(store, []byte{})

	defer iterator.Close()

	for ; iterator.Valid(); iterator.Next() {
		var val types.Power
		k.cdc.MustUnmarshal(iterator.Value(), &val)
		list = append(list, val)
	}

	return
}

func (k Keeper) RemoveAllUpcomingPower(ctx context.Context) {
	existingPower := k.AllUpcomingPower(ctx)

	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, types.KeyPrefix(types.PowerKeyPrefix))

	for _, p := range existingPower {
		store.Delete(types.PowerKey(p.ParticipantAddress))
	}
}
