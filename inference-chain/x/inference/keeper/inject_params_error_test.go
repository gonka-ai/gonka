package keeper

import (
	"context"

	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/productscience/inference/x/inference/types"
)

// CorruptParamsStore writes invalid protobuf bytes to the params key,
// causing InjectParamsIntoContext to fail with an unmarshal error.
// Exported for use in keeper_test package.
func CorruptParamsStore(k Keeper, ctx context.Context) {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store.Set(types.ParamsKey, []byte("corrupted-protobuf-data"))
}
