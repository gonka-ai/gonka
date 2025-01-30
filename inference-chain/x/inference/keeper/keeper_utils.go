package keeper

import (
	"context"
	"cosmossdk.io/store/prefix"
	"fmt"
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

func GetAllValues[T proto.Message](
	ctx context.Context,
	k Keeper,
	keyPrefix []byte,
	newT func() T,
) ([]T, error) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, keyPrefix)

	iterator := store.Iterator(nil, nil)
	defer iterator.Close()

	var results []T
	for ; iterator.Valid(); iterator.Next() {
		bz := iterator.Value()

		val := newT()

		if err := k.cdc.Unmarshal(bz, val); err != nil {
			return nil, fmt.Errorf("failed to unmarshal: %w", err)
		}

		results = append(results, val)
	}

	return results, nil
}
