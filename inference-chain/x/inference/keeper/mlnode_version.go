package keeper

import (
	"context"

	"cosmossdk.io/store/prefix"
	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/productscience/inference/x/inference/types"
)

// SetMLNodeVersion set mlNodeVersion in the store
func (k Keeper) SetMLNodeVersion(ctx context.Context, mlNodeVersion types.MLNodeVersion) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, types.KeyPrefix(types.MLNodeVersionKey))
	b := k.cdc.MustMarshal(&mlNodeVersion)
	store.Set([]byte{0}, b)
}

// GetMLNodeVersion returns mlNodeVersion
func (k Keeper) GetMLNodeVersion(ctx context.Context) (val types.MLNodeVersion, found bool) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, types.KeyPrefix(types.MLNodeVersionKey))

	b := store.Get([]byte{0})
	if b == nil {
		return val, false
	}

	k.cdc.MustUnmarshal(b, &val)
	return val, true
}
