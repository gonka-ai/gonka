package internal

import (
	"context"
	"decentralized-api/cosmosclient"
	"decentralized-api/logging"
	"sync"

	"github.com/productscience/inference/x/inference/types"
)

type EpochGroupDataCache struct {
	mu sync.RWMutex

	cachedEpochIndex uint64
	cachedGroupData  *types.EpochGroupData

	recorder cosmosclient.CosmosMessageClient
}

func NewEpochGroupDataCache(recorder cosmosclient.CosmosMessageClient) *EpochGroupDataCache {
	return &EpochGroupDataCache{
		recorder: recorder,
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
