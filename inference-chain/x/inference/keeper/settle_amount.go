package keeper

import (
	"context"

	"cosmossdk.io/store/prefix"
	storetypes "cosmossdk.io/store/types"
	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/productscience/inference/x/inference/types"
)

// SetSettleAmount set a specific settleAmount in the store from its index
func (k Keeper) SetSettleAmount(ctx context.Context, settleAmount types.SettleAmount) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, types.KeyPrefix(types.SettleAmountKeyPrefix))
	b := k.cdc.MustMarshal(&settleAmount)
	store.Set(types.SettleAmountKey(
		settleAmount.Participant,
	), b)
}

// GetSettleAmount returns a settleAmount from its index
func (k Keeper) GetSettleAmount(
	ctx context.Context,
	participant string,

) (val types.SettleAmount, found bool) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, types.KeyPrefix(types.SettleAmountKeyPrefix))

	b := store.Get(types.SettleAmountKey(
		participant,
	))
	if b == nil {
		return val, false
	}

	k.cdc.MustUnmarshal(b, &val)
	return val, true
}

// RemoveSettleAmount removes a settleAmount from the store
func (k Keeper) RemoveSettleAmount(
	ctx context.Context,
	participant string,

) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, types.KeyPrefix(types.SettleAmountKeyPrefix))
	store.Delete(types.SettleAmountKey(
		participant,
	))
}

// GetAllSettleAmount returns all settleAmount
func (k Keeper) GetAllSettleAmount(ctx context.Context) (list []types.SettleAmount) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, types.KeyPrefix(types.SettleAmountKeyPrefix))
	iterator := storetypes.KVStorePrefixIterator(store, []byte{})

	defer iterator.Close()

	for ; iterator.Valid(); iterator.Next() {
		var val types.SettleAmount
		k.cdc.MustUnmarshal(iterator.Value(), &val)
		list = append(list, val)
	}

	return
}
