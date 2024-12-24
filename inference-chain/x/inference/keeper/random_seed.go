package keeper

import (
	"context"
	"cosmossdk.io/store/prefix"
	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/productscience/inference/x/inference/types"
)

func (k Keeper) SetSeed(ctx context.Context, seed types.RandomSeed) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, types.KeyPrefix(types.RandomSeedKeyPrefix))
	key := types.RandomSeedKey(&seed)

	b := k.cdc.MustMarshal(&seed)
	store.Set(key, b)
}
