package internal

import (
	"context"
	"decentralized-api/cosmosclient"
	"decentralized-api/logging"
	"sync"

	"github.com/productscience/inference/x/inference/types"
)

const maxCachedEpochs = 2

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

	recorder cosmosclient.CosmosMessageClient
}

func NewEpochGroupDataCache(recorder cosmosclient.CosmosMessageClient) *EpochGroupDataCache {
	return &EpochGroupDataCache{
		recorder:   recorder,
		epochCache: make(map[uint64]*cachedEpochData),
	}
}

func (c *EpochGroupDataCache) GetCurrentEpochGroupData(currentEpochIndex uint64) (*types.EpochGroupData, error) {
	c.mu.RLock()
	if c.cachedGroupData != nil && c.cachedEpochIndex == currentEpochIndex {
		defer c.mu.RUnlock()
		return c.cachedGroupData, nil
	}
	c.mu.RUnlock()

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.cachedGroupData != nil && c.cachedEpochIndex == currentEpochIndex {
		return c.cachedGroupData, nil
	}

	logging.Info("Fetching new epoch group data", types.Config,
		"cachedEpochIndex", c.cachedEpochIndex, "currentEpochIndex", currentEpochIndex)

	queryClient := c.recorder.NewInferenceQueryClient()
	req := &types.QueryCurrentEpochGroupDataRequest{}
	resp, err := queryClient.CurrentEpochGroupData(context.Background(), req)
	if err != nil {
		logging.Warn("Failed to query current epoch group data", types.Config, "error", err)
		return nil, err
	}

	c.cachedEpochIndex = currentEpochIndex
	c.cachedGroupData = &resp.EpochGroupData

	logging.Info("Updated epoch group data cache", types.Config,
		"epochIndex", currentEpochIndex,
		"validationWeights", len(resp.EpochGroupData.ValidationWeights))

	return c.cachedGroupData, nil
}

// GetEpochGroupData returns epoch group data for specific epoch.
// Uses cache, queries chain only on cache miss. Keeps max 2 epochs.
func (c *EpochGroupDataCache) GetEpochGroupData(ctx context.Context, epochIndex uint64) (*types.EpochGroupData, error) {
	c.mu.RLock()
	if cached, ok := c.epochCache[epochIndex]; ok {
		c.mu.RUnlock()
		return cached.data, nil
	}
	c.mu.RUnlock()

	c.mu.Lock()
	defer c.mu.Unlock()

	// Double-check after acquiring write lock
	if cached, ok := c.epochCache[epochIndex]; ok {
		return cached.data, nil
	}

	logging.Debug("Fetching epoch group data", types.Config, "epochIndex", epochIndex)

	queryClient := c.recorder.NewInferenceQueryClient()
	resp, err := queryClient.EpochGroupData(ctx, &types.QueryGetEpochGroupDataRequest{
		EpochIndex: epochIndex,
	})
	if err != nil {
		return nil, err
	}

	// Prune if needed (keep max 2 epochs)
	if len(c.epochCache) >= maxCachedEpochs {
		c.pruneOldest(epochIndex)
	}

	// Build address set for O(1) lookups
	addressSet := make(map[string]struct{}, len(resp.EpochGroupData.ValidationWeights))
	for _, vw := range resp.EpochGroupData.ValidationWeights {
		addressSet[vw.MemberAddress] = struct{}{}
	}

	c.epochCache[epochIndex] = &cachedEpochData{
		data:       &resp.EpochGroupData,
		addressSet: addressSet,
	}

	logging.Debug("Cached epoch group data", types.Config,
		"epochIndex", epochIndex,
		"participants", len(addressSet))

	return &resp.EpochGroupData, nil
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
