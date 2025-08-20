package keeper

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"cosmossdk.io/store/prefix"
	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/productscience/inference/x/inference/types"
)

func (k Keeper) SetPocBatch(ctx context.Context, batch types.PoCBatch) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, types.KeyPrefix(types.PocBatchKeyPrefix))
	key := types.PoCBatchKey(batch.PocStageStartBlockHeight, batch.ParticipantAddress, batch.BatchId)

	k.LogInfo("PoC: Storing batch", types.PoC, "key", key, "batch", batch)

	b := k.cdc.MustMarshal(&batch)
	store.Set(key, b)
}

// TODO: RENAME GetPoCBatchesByStage > ByEpoch
func (k Keeper) GetPoCBatchesByStage(ctx context.Context, pocStageStartBlockHeight int64) (map[string][]types.PoCBatch, error) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	prefixKey := append(types.KeyPrefix(types.PocBatchKeyPrefix), []byte(strconv.FormatInt(pocStageStartBlockHeight, 10)+"/")...)

	store := prefix.NewStore(storeAdapter, prefixKey)

	iterator := store.Iterator(nil, nil)
	defer iterator.Close()

	batches := make(map[string][]types.PoCBatch)

	for ; iterator.Valid(); iterator.Next() {
		key := string(iterator.Key())
		value := iterator.Value()

		// Trim the trailing "/" and split the key to extract participantIndex and batchId
		trimmedKey := strings.TrimSuffix(key, "/")
		segments := strings.Split(trimmedKey, "/")

		// Validate the key format
		if len(segments) != 2 {
			return nil, fmt.Errorf("invalid key format: %s", key)
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

func (k Keeper) GetPoCBatchesCountByStage(ctx context.Context, pocStageStartBlockHeight int64) (uint64, error) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	prefixKey := append(types.KeyPrefix(types.PocBatchKeyPrefix), []byte(strconv.FormatInt(pocStageStartBlockHeight, 10)+"/")...)

	store := prefix.NewStore(storeAdapter, prefixKey)
	iterator := store.Iterator(nil, nil)
	defer iterator.Close()
	count := uint64(0)
	for ; iterator.Valid(); iterator.Next() {
		count++
	}

	return count, nil
}

func (k Keeper) SetPoCValidation(ctx context.Context, validation types.PoCValidation) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, types.KeyPrefix(types.PocValidationPrefix))
	key := types.PoCValidationKey(validation.PocStageStartBlockHeight, validation.ParticipantAddress, validation.ValidatorParticipantAddress)

	k.LogInfo("PoC: Storing validation", types.PoC, "key", key, "validation", validation)

	b := k.cdc.MustMarshal(&validation)
	store.Set(key, b)
}

func (k Keeper) GetPoCValidationByStage(ctx context.Context, pocStageStartBlockHeight int64) (map[string][]types.PoCValidation, error) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	prefixKey := append(types.KeyPrefix(types.PocValidationPrefix), []byte(strconv.FormatInt(pocStageStartBlockHeight, 10)+"/")...)

	store := prefix.NewStore(storeAdapter, prefixKey)
	iterator := store.Iterator(nil, nil)
	defer iterator.Close()

	validations := make(map[string][]types.PoCValidation)

	for ; iterator.Valid(); iterator.Next() {
		key := string(iterator.Key())
		value := iterator.Value()

		trimmedKey := strings.TrimSuffix(key, "/")
		segments := strings.Split(trimmedKey, "/")

		// Validate the key format
		if len(segments) != 2 {
			return nil, fmt.Errorf("invalid key format: %s", key)
		}

		participantIndex := segments[0]
		// valParticipantIndex := segments[1]

		var validation types.PoCValidation
		if err := k.cdc.Unmarshal(value, &validation); err != nil {
			return nil, fmt.Errorf("failed to unmarshal PoCBatch: %w", err)
		}

		validations[participantIndex] = append(validations[participantIndex], validation)
	}

	return validations, nil
}

func (k Keeper) GetPocValidationCountByStage(ctx context.Context, pocStageStartBlockHeight int64) (uint64, error) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	prefixKey := append(types.KeyPrefix(types.PocValidationPrefix), []byte(strconv.FormatInt(pocStageStartBlockHeight, 10)+"/")...)

	store := prefix.NewStore(storeAdapter, prefixKey)
	iterator := store.Iterator(nil, nil)
	defer iterator.Close()
	count := uint64(0)
	for ; iterator.Valid(); iterator.Next() {
		count++
	}

	return count, nil
}
