package keeper

import (
	"context"
	"cosmossdk.io/store/prefix"
	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/productscience/inference/x/inference/types"
	"strconv"
)

func (k Keeper) SetPocBatch(ctx context.Context, batch types.PoCBatch) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, types.KeyPrefix(types.PocBatchKeyPrefix))
	key := types.PoCBatchKey(batch.PocStageStartBlockHeight, batch.ParticipantAddress, types.GenerateBatchID())

	k.LogInfo("PoC: Storing batch", "key", key, "batch", batch)

	b := k.cdc.MustMarshal(&batch)
	store.Set(key, b)
}

func (k Keeper) GetBatchesByPoCStage(ctx context.Context, pocStageStartBlockHeight int64) (map[string][]types.PoCBatch, error) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	prefixKey := append(types.KeyPrefix(types.PocBatchKeyPrefix), []byte(strconv.FormatInt(pocStageStartBlockHeight, 10)+"/")...)

	store := prefix.NewStore(storeAdapter, prefixKey)

	iterator := store.Iterator(nil, nil)
	defer iterator.Close()

	var batches map[string][]types.PoCBatch

	for ; iterator.Valid(); iterator.Next() {
		key := iterator.Key()
		value := iterator.Value()
		_ = key

		var batch types.PoCBatch
		err := k.cdc.Unmarshal(value, &batch)
		if err != nil {
			return nil, err
		}

		batches[key] = append(batches[key], batch)
	}

	return batches, nil
}
