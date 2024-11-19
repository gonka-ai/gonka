package keeper

import (
	"context"
	"cosmossdk.io/store/prefix"
	"fmt"
	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/productscience/inference/x/inference/types"
	"strconv"
	"strings"
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

	batches := make(map[string][]types.PoCBatch)

	for ; iterator.Valid(); iterator.Next() {
		key := iterator.Key()
		value := iterator.Value()
		// Convert the key from []byte to string for easier manipulation
		keyStr := string(key)

		// Trim the trailing "/" and split the key to extract participantIndex and batchId
		trimmedKey := strings.TrimSuffix(keyStr, "/")
		segments := strings.Split(trimmedKey, "/")

		// Validate the key format
		if len(segments) != 2 {
			return nil, fmt.Errorf("invalid key format: %s", keyStr)
		}

		participantIndex := segments[0]
		// batchId := segments[1] // Uncomment if you need to use batchId

		// Unmarshal the value into a PoCBatch struct
		var batch types.PoCBatch
		if err := k.cdc.Unmarshal(value, &batch); err != nil {
			return nil, fmt.Errorf("failed to unmarshal PoCBatch: %w", err)
		}

		// Append the batch to the corresponding participant's slice
		batches[participantIndex] = append(batches[participantIndex], batch)
	}

	return batches, nil
}
