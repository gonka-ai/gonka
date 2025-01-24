package keeper

import (
	"context"
	"cosmossdk.io/store/prefix"
	"fmt"
	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/cosmos/gogoproto/proto"
	"reflect"
)

func SetValue[T proto.Message](k Keeper, ctx context.Context, object T, keyPrefix []byte, key []byte) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, keyPrefix)
	b := k.cdc.MustMarshal(object)
	store.Set(key, b)
}

func GetValue[T proto.Message](k Keeper, ctx context.Context, keyPrefix []byte, key []byte) (T, bool) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, keyPrefix)

	bz := store.Get(key)
	var object T
	if bz == nil {
		return object, false
	}

	k.cdc.MustUnmarshal(bz, object)

	return object, true
}

type ExtractKeyFunc func(string) (string, error)

func ListValues[T proto.Message](k Keeper, ctx context.Context, keyPrefix []byte, extractKeyFunc ExtractKeyFunc) (map[string]T, error) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))

	store := prefix.NewStore(storeAdapter, keyPrefix)

	iterator := store.Iterator(nil, nil)
	defer iterator.Close()

	result := make(map[string]T)

	for ; iterator.Valid(); iterator.Next() {
		key := string(iterator.Key())
		value := iterator.Value()

		resultKey, err := extractKeyFunc(key)
		if err != nil {
			return nil, fmt.Errorf("ListValues: failed to extract key. key = %s. err = %w", key, err)
		}

		var object T
		if err := k.cdc.Unmarshal(value, object); err != nil {
			tType := reflect.TypeOf(object)
			return nil, fmt.Errorf("ListValues: failed to unmarshal. type = %s. err = %w", tType, err)
		}

		result[resultKey] = object
	}

	return result, nil
}
