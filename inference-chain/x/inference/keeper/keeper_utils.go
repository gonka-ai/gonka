package keeper

import (
	"context"
	"cosmossdk.io/store/prefix"
	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/cosmos/gogoproto/proto"
)

func PrefixStore(ctx context.Context, k Keeper, ketPrefix []byte) *prefix.Store {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, ketPrefix)
	return &store
}

func SetValue[T proto.Message](k Keeper, ctx context.Context, object T, keyPrefix []byte, key []byte) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, keyPrefix)
	b := k.cdc.MustMarshal(object)
	store.Set(key, b)
}

func GetValue[T proto.Message](k Keeper, ctx context.Context, object T, keyPrefix []byte, key []byte) (T, bool) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, keyPrefix)

	bz := store.Get(key)
	if bz == nil {
		return object, false
	}

	k.cdc.MustUnmarshal(bz, object)

	return object, true
}
