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
	// A ByEpoch value holds every inference ID of one developer and epoch (up to ~6 MB
	// on mainnet); deleting one takes about as long as 400 small keys.
	DeveloperStatsByEpochRemoveCost = int64(400)
)

type developerStatsPruningTarget struct {
	prefix      string
	maxPerBlock int64
	cost        int64 // shared budget units per key
}

var developerStatsPruningTargets = []developerStatsPruningTarget{
	// Each value holds all inference IDs of one developer and epoch and can be large:
	// one per block, and first, so it does not wait for the index prefixes to drain.
	{prefix: StatsDevelopersByEpoch, maxPerBlock: DeveloperStatsByEpochPruningMaxPerBlock, cost: DeveloperStatsByEpochRemoveCost},
	{prefix: StatsDevelopersByInferenceAndModel, maxPerBlock: DeveloperStatsPruningMaxPerBlock, cost: 1},
	{prefix: StatsDevelopersByInference, maxPerBlock: DeveloperStatsPruningMaxPerBlock, cost: 1},
	{prefix: StatsDevelopersByTime, maxPerBlock: DeveloperStatsPruningMaxPerBlock, cost: 1},
}

// PruneDeveloperStats gradually removes the obsolete pre-devshard statistics.
// Production stopped writing these prefixes when developer stats moved off-chain,
// so the remaining keys themselves are sufficient progress tracking.
func (k Keeper) PruneDeveloperStats(ctx context.Context) (int64, error) {
	pruned, _, err := k.pruneDeveloperStats(ctx, -1)
	return pruned, err
}

// pruneDeveloperStats removes at most DeveloperStatsPruningMaxPerBlock keys and, when
// budget >= 0, at most budget units. It returns the keys removed and the units used.
func (k Keeper) pruneDeveloperStats(ctx context.Context, budget int64) (int64, int64, error) {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	pruned, used := int64(0), int64(0)

	for _, target := range developerStatsPruningTargets {
		remaining := DeveloperStatsPruningMaxPerBlock - pruned
		if remaining <= 0 {
			break
		}

		limit := min(remaining, target.maxPerBlock)
		if budget >= 0 {
			limit = min(limit, (budget-used)/target.cost)
			if limit <= 0 {
				continue // a cheaper target may still fit
			}
		}
		targetStore := prefix.NewStore(store, types.KeyPrefix(target.prefix))
		iter := targetStore.Iterator(nil, nil)
		keysToDelete := make([][]byte, 0, limit)
		for ; iter.Valid() && int64(len(keysToDelete)) < limit; iter.Next() {
			keysToDelete = append(keysToDelete, append([]byte(nil), iter.Key()...))
		}
		if err := iter.Close(); err != nil {
			return pruned, used, err
		}
		for _, key := range keysToDelete {
			targetStore.Delete(key)
		}

		prunedFromTarget := int64(len(keysToDelete))
		pruned += prunedFromTarget
		used += prunedFromTarget * target.cost
	}

	if pruned > 0 {
		k.LogDebug("Pruned legacy developer stats", types.Pruning, "pruned", pruned)
	}
	return pruned, used, nil
}

// developerStatsPruner runs PruneDeveloperStats in the shared Prune rotation.
type developerStatsPruner struct{}

func (developerStatsPruner) prune(ctx context.Context, k Keeper, _ int64, budget *int64) error {
	if budget == nil {
		_, err := k.PruneDeveloperStats(ctx)
		return err
	}
	_, used, err := k.pruneDeveloperStats(ctx, max(*budget, 0))
	*budget -= used
	return err
}
