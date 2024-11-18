package keeper

import (
	"context"
	"cosmossdk.io/store/prefix"
	storetypes "cosmossdk.io/store/types"
	"github.com/cosmos/cosmos-sdk/runtime"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

const (
	EpochPolicyKey  = "epoch-policy"
	EpochGroupIdKey = "epoch-group-id"
)

func (k Keeper) SetEpochPolicy(ctx context.Context, policyAddress string) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, types.KeyPrefix(types.EpochGroupPrefix))

	key := []byte(EpochPolicyKey)
	value := []byte(policyAddress)
	store.Set(key, value)
}

func (k Keeper) GetEpochPolicy(ctx context.Context) (policyAddress string) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, types.KeyPrefix(types.EpochGroupPrefix))

	key := []byte(EpochPolicyKey)
	value := store.Get(key)

	if value == nil {
		return ""
	}

	return string(value)
}

func (k Keeper) SetEpochGroupId(ctx context.Context, groupId uint64) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, types.KeyPrefix(types.EpochGroupPrefix))

	key := []byte(EpochGroupIdKey)
	value := sdk.Uint64ToBigEndian(groupId)
	store.Set(key, value)
}

func (k Keeper) GetEpochGroupId(ctx context.Context) (groupId uint64) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, types.KeyPrefix(types.EpochGroupPrefix))

	key := []byte(EpochGroupIdKey)
	value := store.Get(key)

	if value == nil {
		return 0
	}

	return sdk.BigEndianToUint64(value)
}

func (k Keeper) SetPower(ctx context.Context, power types.Power) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, types.KeyPrefix(types.PowerKeyPrefix))
	key := types.PowerKey(power.ParticipantAddress)

	b := k.cdc.MustMarshal(&power)
	store.Set(key, b)
}

func (k Keeper) AllPower(ctx context.Context) (list []types.Power) {
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

func (k Keeper) RemoveAllPower(ctx context.Context) {
	existingPower := k.AllPower(ctx)

	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, types.KeyPrefix(types.PowerKeyPrefix))

	for _, p := range existingPower {
		store.Delete(types.PowerKey(p.ParticipantAddress))
	}
}
