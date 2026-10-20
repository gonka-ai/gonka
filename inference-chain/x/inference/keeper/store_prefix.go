package keeper

import (
	"bytes"
	"context"

	"cosmossdk.io/collections"
	"cosmossdk.io/store/prefix"
	"github.com/cosmos/cosmos-sdk/runtime"
)

func (k Keeper) ClearStorePrefix(ctx context.Context, p collections.Prefix) (int, error) {
	store := prefix.NewStore(runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx)), p.Bytes())
	iter := store.Iterator(nil, nil)
	var keys [][]byte
	for ; iter.Valid(); iter.Next() {
		keys = append(keys, bytes.Clone(iter.Key()))
	}
	if err := iter.Close(); err != nil {
		return 0, err
	}
	for _, key := range keys {
		store.Delete(key)
	}
	return len(keys), nil
}
