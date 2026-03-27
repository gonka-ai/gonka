package keeper

import (
	"context"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/gogoproto/proto"
	"github.com/productscience/inference/x/inference/types"
)

// ensureRandomSeedCacheInited lazily inits the warm cache from the current effective epoch.
// Entries are filled on first GetRandomSeed or SetRandomSeed for the current epoch. Caller must hold no cache lock.
// inited is only ever set true (never false), so after this returns the cache cannot become uninitialized before the caller takes the lock.
func (k Keeper) ensureRandomSeedCacheInited(ctx context.Context) {
	c := k.randomSeedCache
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.inited {
		return
	}
	eff, ok := k.GetEffectiveEpochIndex(ctx)
	if !ok {
		return
	}
	c.current = eff
	c.m = make(map[randomSeedCacheKey]types.RandomSeed)
	c.inited = true
}

// refreshRandomSeedCache sets the current epoch and clears the cache. Call after SetEffectiveEpochIndex.
func (k Keeper) refreshRandomSeedCache(newEffective uint64) {
	c := k.randomSeedCache
	c.mu.Lock()
	defer c.mu.Unlock()
	c.current = newEffective
	c.m = make(map[randomSeedCacheKey]types.RandomSeed)
	c.inited = true
}

func (k Keeper) SetRandomSeed(ctx context.Context, seed types.RandomSeed) error {
	addr, err := sdk.AccAddressFromBech32(seed.Participant)
	if err != nil {
		return err
	}
	pk := collections.Join(seed.EpochIndex, addr)
	if err := k.RandomSeeds.Set(ctx, pk, seed); err != nil {
		return err
	}
	k.ensureRandomSeedCacheInited(ctx)
	c := k.randomSeedCache
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.inited && seed.EpochIndex == c.current {
		key := randomSeedCacheKey{Epoch: seed.EpochIndex, Participant: seed.Participant}
		cloned := proto.Clone(&seed).(*types.RandomSeed)
		c.m[key] = *cloned
	}
	return nil
}

func (k Keeper) GetRandomSeed(ctx context.Context, epochIndex uint64, participantAddress string) (types.RandomSeed, bool) {
	addr, err := sdk.AccAddressFromBech32(participantAddress)
	if err != nil {
		return types.RandomSeed{}, false
	}
	k.ensureRandomSeedCacheInited(ctx)
	c := k.randomSeedCache
	key := randomSeedCacheKey{Epoch: epochIndex, Participant: participantAddress}
	c.mu.RLock()
	if c.inited && epochIndex == c.current {
		if cached, ok := c.m[key]; ok {
			c.mu.RUnlock()
			cloned := proto.Clone(&cached).(*types.RandomSeed)
			return *cloned, true
		}
	}
	c.mu.RUnlock()
	pk := collections.Join(epochIndex, addr)
	v, err := k.RandomSeeds.Get(ctx, pk)
	if err != nil {
		return types.RandomSeed{}, false
	}
	c.mu.Lock()
	if c.inited && epochIndex == c.current {
		cloned := proto.Clone(&v).(*types.RandomSeed)
		c.m[key] = *cloned
	}
	c.mu.Unlock()
	return v, true
}

// GetParticipantEpochSeed retrieves the seed value for a participant in a given epoch.
// Returns the seed value and a boolean indicating if it was found.
func (k Keeper) GetParticipantEpochSeed(ctx context.Context, epochIndex uint64, participantAddress string) (int64, bool) {
	randomSeed, found := k.GetRandomSeed(ctx, epochIndex, participantAddress)
	if !found {
		return 0, false
	}
	return randomSeed.Seed, true
}
