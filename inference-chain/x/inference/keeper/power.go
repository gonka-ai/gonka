package keeper

import (
	"context"
	"cosmossdk.io/store/prefix"
	storetypes "cosmossdk.io/store/types"
	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/productscience/inference/x/inference/types"
)

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
