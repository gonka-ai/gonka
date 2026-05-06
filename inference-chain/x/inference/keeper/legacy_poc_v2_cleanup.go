package keeper

import (
	"context"

	"cosmossdk.io/store/prefix"
	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/productscience/inference/x/inference/types"
)

// ClearLegacyPoCv2Data removes all raw KV entries under the legacy PoC v2
// prefixes (38, 39, 40). These collections changed key codec in v0.2.12 --
// model_id was added to the key -- and were moved to new prefixes (58, 59, 60).
// The old entries cannot be decoded with the new codec, so collections.Map.Clear
// on the new collections is insufficient. This function uses raw store iteration
// to delete the old bytes.
func (k Keeper) ClearLegacyPoCv2Data(ctx context.Context) error {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	for _, keyPrefix := range [][]byte{
		types.LegacyPoCValidationV2Prefix,
		types.LegacyPoCV2StoreCommitPrefix,
		types.LegacyMLNodeWeightDistributionPrefix,
	} {
		prefixStore := prefix.NewStore(store, keyPrefix)
		iterator := prefixStore.Iterator(nil, nil)
		var keysToDelete [][]byte
		for ; iterator.Valid(); iterator.Next() {
			key := iterator.Key()
			keyCopy := make([]byte, len(key))
			copy(keyCopy, key)
			keysToDelete = append(keysToDelete, keyCopy)
		}
		iterator.Close()

		for _, key := range keysToDelete {
			prefixStore.Delete(key)
		}
	}
	return nil
}
