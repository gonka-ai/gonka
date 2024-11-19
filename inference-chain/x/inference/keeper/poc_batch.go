package keeper

import (
	"context"
	"cosmossdk.io/store/prefix"
	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/productscience/inference/x/inference/types"
)

func (k Keeper) SetPocBatch(ctx context.Context, batch types.PoCBatch) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, types.KeyPrefix(types.PocBatchKeyPrefix))
	key := types.PoCBatchKey(batch.PocStageStartBlockHeight, batch.ParticipantAddress, types.GenerateBatchID())

	k.LogInfo("PoC: Storing batch", "key", key, "batch", batch)

	b := k.cdc.MustMarshal(&batch)
	store.Set(key, b)
}
