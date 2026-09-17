package keeper

import (
	"context"

	"cosmossdk.io/store/prefix"
	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/productscience/inference/x/inference/types"
)

const (
	DeveloperStatsPruningMaxPerBlock        = int64(1000)
	DeveloperStatsByEpochPruningMaxPerBlock = int64(1)
)

type developerStatsPruningTarget struct {
	prefix      string
	maxPerBlock int64
}

var developerStatsPruningTargets = []developerStatsPruningTarget{
	{prefix: StatsDevelopersByInferenceAndModel, maxPerBlock: DeveloperStatsPruningMaxPerBlock},
	{prefix: StatsDevelopersByInference, maxPerBlock: DeveloperStatsPruningMaxPerBlock},
	{prefix: StatsDevelopersByTime, maxPerBlock: DeveloperStatsPruningMaxPerBlock},
	// This prefix has relatively few keys, but each value contains all inference
	// IDs for one developer and epoch and can be large. Delete only one per block.
	{prefix: StatsDevelopersByEpoch, maxPerBlock: DeveloperStatsByEpochPruningMaxPerBlock},
}

// PruneDeveloperStats gradually removes the obsolete pre-devshard statistics.
// Production stopped writing these prefixes when developer stats moved off-chain,
// so the remaining keys themselves are sufficient progress tracking.
func (k Keeper) PruneDeveloperStats(ctx context.Context) (int64, error) {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	pruned := int64(0)

	for _, target := range developerStatsPruningTargets {
		remaining := DeveloperStatsPruningMaxPerBlock - pruned
		if remaining <= 0 {
			break
		}

		limit := min(remaining, target.maxPerBlock)
		targetStore := prefix.NewStore(store, types.KeyPrefix(target.prefix))
		iter := targetStore.Iterator(nil, nil)
		keysToDelete := make([][]byte, 0, limit)
		for ; iter.Valid() && int64(len(keysToDelete)) < limit; iter.Next() {
			keysToDelete = append(keysToDelete, append([]byte(nil), iter.Key()...))
		}
		if err := iter.Close(); err != nil {
			return pruned, err
		}
		for _, key := range keysToDelete {
			targetStore.Delete(key)
		}

		prunedFromTarget := int64(len(keysToDelete))
		pruned += prunedFromTarget
		if prunedFromTarget == target.maxPerBlock {
			break
		}
	}

	if pruned > 0 {
		k.LogDebug("Pruned legacy developer stats", types.Pruning, "pruned", pruned)
	}
	return pruned, nil
}
