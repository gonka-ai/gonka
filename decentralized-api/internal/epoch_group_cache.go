package internal

import (
	"context"
	"decentralized-api/cosmosclient"
	"decentralized-api/logging"
	"strconv"
	"sync"
	"time"

	"github.com/productscience/inference/x/inference/types"
	"golang.org/x/sync/singleflight"
)

const maxCachedEpochs = 2

// epochGroupQueryTimeout bounds the chain query for the current epoch group data.
// The cometbft/cosmos client sets no default timeout, so without this a stalled
// node would hang the fetch indefinitely.
const epochGroupQueryTimeout = 15 * time.Second

type cachedEpochData struct {
	data       *types.EpochGroupData
	addressSet map[string]struct{} // O(1) lookup for active participants
}

type EpochGroupDataCache struct {
	mu sync.RWMutex

	// Legacy single-epoch cache for GetCurrentEpochGroupData
	cachedEpochIndex uint64
	cachedGroupData  *types.EpochGroupData

	// Multi-epoch cache for GetEpochGroupData (max 2 epochs)
	epochCache map[uint64]*cachedEpochData

	// Singleflight groups coalesce concurrent cache misses for the same epoch
	// into a single chain query. Combined with running the RPC outside c.mu,
	// this prevents an epoch-boundary thundering herd and stops one slow query
	// from pinning the cache mutex and stalling every caller.
	currentGroupSF singleflight.Group
	epochGroupSF   singleflight.Group

	recorder cosmosclient.CosmosMessageClient
}

func NewEpochGroupDataCache(recorder cosmosclient.CosmosMessageClient) *EpochGroupDataCache {
	return &EpochGroupDataCache{
		recorder:   recorder,
		epochCache: make(map[uint64]*cachedEpochData),
	}
}

func (c *EpochGroupDataCache) GetCurrentEpochGroupData(currentEpochIndex uint64) (*types.EpochGroupData, error) {
	// Fast path: cache hit under the read lock.
	c.mu.RLock()
	if c.cachedGroupData != nil && c.cachedEpochIndex == currentEpochIndex {
		data := c.cachedGroupData
		c.mu.RUnlock()
		return data, nil
	}
	c.mu.RUnlock()

	// Miss: coalesce concurrent fetches for this epoch into one RPC, and run the
	// RPC WITHOUT holding c.mu so a slow/hung chain query cannot pin the mutex
	// and stall every inference-validation caller.
	key := strconv.FormatUint(currentEpochIndex, 10)
	result, err, _ := c.currentGroupSF.Do(key, func() (interface{}, error) {
		// Re-check: another goroutine may have populated the cache while we were
		// becoming the singleflight leader.
		c.mu.RLock()
		prevIndex := c.cachedEpochIndex
		if c.cachedGroupData != nil && c.cachedEpochIndex == currentEpochIndex {
			data := c.cachedGroupData
			c.mu.RUnlock()
			return data, nil
		}
		c.mu.RUnlock()

		logging.Info("Fetching new epoch group data", types.Config,
			"cachedEpochIndex", prevIndex, "currentEpochIndex", currentEpochIndex)

		ctx, cancel := context.WithTimeout(context.Background(), epochGroupQueryTimeout)
		defer cancel()
		queryClient := c.recorder.NewInferenceQueryClient()
		resp, err := queryClient.CurrentEpochGroupData(ctx, &types.QueryCurrentEpochGroupDataRequest{})
		if err != nil {
			logging.Warn("Failed to query current epoch group data", types.Config, "error", err)
			return nil, err
		}

		data := &resp.EpochGroupData
		c.mu.Lock()
		c.cachedEpochIndex = currentEpochIndex
		c.cachedGroupData = data
		c.mu.Unlock()

		logging.Info("Updated epoch group data cache", types.Config,
			"epochIndex", currentEpochIndex,
			"validationWeights", len(resp.EpochGroupData.ValidationWeights))

		return data, nil
	})
	if err != nil {
		return nil, err
	}
	return result.(*types.EpochGroupData), nil
}

// GetEpochGroupData returns epoch group data for specific epoch.
// Uses cache, queries chain only on cache miss. Keeps max 2 epochs.
func (c *EpochGroupDataCache) GetEpochGroupData(ctx context.Context, epochIndex uint64) (*types.EpochGroupData, error) {
	// Fast path: cache hit under the read lock.
	c.mu.RLock()
	if cached, ok := c.epochCache[epochIndex]; ok {
		data := cached.data
		c.mu.RUnlock()
		return data, nil
	}
	c.mu.RUnlock()

	// Miss: coalesce and run the RPC outside c.mu (see GetCurrentEpochGroupData).
	key := strconv.FormatUint(epochIndex, 10)
	result, err, _ := c.epochGroupSF.Do(key, func() (interface{}, error) {
		c.mu.RLock()
		if cached, ok := c.epochCache[epochIndex]; ok {
			data := cached.data
			c.mu.RUnlock()
			return data, nil
		}
		c.mu.RUnlock()

		logging.Debug("Fetching epoch group data", types.Config, "epochIndex", epochIndex)

		queryClient := c.recorder.NewInferenceQueryClient()
		resp, err := queryClient.EpochGroupData(ctx, &types.QueryGetEpochGroupDataRequest{
			EpochIndex: epochIndex,
		})
		if err != nil {
			return nil, err
		}

		// Build address set for O(1) lookups.
		addressSet := make(map[string]struct{}, len(resp.EpochGroupData.ValidationWeights))
		for _, vw := range resp.EpochGroupData.ValidationWeights {
			addressSet[vw.MemberAddress] = struct{}{}
		}
		data := &resp.EpochGroupData

		c.mu.Lock()
		// Prune if needed (keep max 2 epochs).
		if len(c.epochCache) >= maxCachedEpochs {
			c.pruneOldest(epochIndex)
		}
		c.epochCache[epochIndex] = &cachedEpochData{
			data:       data,
			addressSet: addressSet,
		}
		c.mu.Unlock()

		logging.Debug("Cached epoch group data", types.Config,
			"epochIndex", epochIndex,
			"participants", len(addressSet))

		return data, nil
	})
	if err != nil {
		return nil, err
	}
	return result.(*types.EpochGroupData), nil
}

// IsActiveParticipant checks if address is active at given epoch. O(1) lookup.
func (c *EpochGroupDataCache) IsActiveParticipant(ctx context.Context, epochIndex uint64, address string) (bool, error) {
	c.mu.RLock()
	if cached, ok := c.epochCache[epochIndex]; ok {
		_, exists := cached.addressSet[address]
		c.mu.RUnlock()
		return exists, nil
	}
	c.mu.RUnlock()

	// Cache miss - fetch data first
	_, err := c.GetEpochGroupData(ctx, epochIndex)
	if err != nil {
		return false, err
	}

	// Now check again
	c.mu.RLock()
	defer c.mu.RUnlock()
	if cached, ok := c.epochCache[epochIndex]; ok {
		_, exists := cached.addressSet[address]
		return exists, nil
	}
	return false, nil
}

// pruneOldest removes epochs older than currentEpoch - 1
func (c *EpochGroupDataCache) pruneOldest(currentEpoch uint64) {
	for epochId := range c.epochCache {
		if epochId < currentEpoch-1 {
			delete(c.epochCache, epochId)
			logging.Debug("Pruned old epoch from cache", types.Config, "epochId", epochId)
		}
	}
}
