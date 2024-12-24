package keeper

import (
	"context"
	"cosmossdk.io/store/prefix"
	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/productscience/inference/x/inference/types"
)

func (k Keeper) SetRandomSeed(ctx context.Context, seed types.RandomSeed) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, types.KeyPrefix(types.RandomSeedKeyPrefix))
	key := types.RandomSeedKey(seed.BlockHeight, seed.Participant)

	b := k.cdc.MustMarshal(&seed)
	store.Set(key, b)
}

func (k Keeper) GetRandomSeed(ctx context.Context, blockHeight int64, participantAddress string) (types.RandomSeed, bool) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, types.KeyPrefix(types.RandomSeedKeyPrefix))
	key := types.RandomSeedKey(blockHeight, participantAddress)

	bz := store.Get(key)
	if bz == nil {
		return types.RandomSeed{}, false
	}

	var seed types.RandomSeed
	k.cdc.MustUnmarshal(bz, &seed)

	return seed, true
}
