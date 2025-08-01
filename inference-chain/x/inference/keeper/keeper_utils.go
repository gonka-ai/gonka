package keeper

import (
	"context"
	"cosmossdk.io/store/prefix"
	"fmt"
	"github.com/cosmos/cosmos-sdk/runtime"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/gogoproto/proto"
)

func EmptyPrefixStore(ctx context.Context, k *Keeper) *prefix.Store {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, []byte{})
	return &store
}

func PrefixStore(ctx context.Context, k *Keeper, keyPrefix []byte) *prefix.Store {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, keyPrefix)
	return &store
}

func SetValue[T proto.Message](k Keeper, ctx context.Context, object T, keyPrefix []byte, key []byte) {
	// For some reason IDE syntax highlighting shows it's OK,
	// but I get a compiler error:
	//   "invalid operation: object == nil (mismatched types T and untyped nil)"
	/*	if object == nil {
		k.LogError("SetValue called with nil object, returning", types.System)
		return
	}*/

	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, keyPrefix)
	b := k.cdc.MustMarshal(object)
	store.Set(key, b)
}

func SetUint64Value(k *Keeper, ctx context.Context, keyPrefix []byte, key []byte, value uint64) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, keyPrefix)
	b := sdk.Uint64ToBigEndian(value)
	store.Set(key, b)
}

func GetUint64Value(k *Keeper, ctx context.Context, keyPrefix []byte, key []byte) (uint64, bool) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, keyPrefix)
	bz := store.Get(key)
	if bz == nil {
		return 0, false
	}

	return sdk.BigEndianToUint64(bz), true
}

func GetValue[T proto.Message](k *Keeper, ctx context.Context, object T, keyPrefix []byte, key []byte) (T, bool) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, keyPrefix)

	return GetValueFromStore(k, object, store, key)
}

func DeleteValue(k *Keeper, ctx context.Context, keyPrefix []byte, key []byte) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, keyPrefix)

	store.Delete(key)
}

func GetValueFromStore[T proto.Message](k *Keeper, object T, store prefix.Store, key []byte) (T, bool) {
	bz := store.Get(key)
	if bz == nil {
		return object, false
	}

	k.cdc.MustUnmarshal(bz, object)

	return object, true
}

func GetAllValues[T proto.Message](
	ctx context.Context,
	k *Keeper,
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

func PointersToValues[T any](pointerSlice []*T) ([]T, error) {
	values := make([]T, len(pointerSlice))
	for i, ptr := range pointerSlice {
		if ptr == nil {
			return nil, fmt.Errorf("nil pointer at index %d", i)
		}
		values[i] = *ptr
	}
	return values, nil
}

func ValuesToPointers[T any](valueSlice []T) []*T {
	pointerSlice := make([]*T, len(valueSlice))
	for i, val := range valueSlice {
		pointerSlice[i] = &val
	}
	return pointerSlice
}
