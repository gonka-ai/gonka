package internal

import (
	"common/logging"
	"context"
	"decentralized-api/cosmosclient"
	"fmt"
	"sync"

	"github.com/productscience/inference/x/inference/types"
)

// EpochModelThreshold is a per-model inference validation threshold for an epoch,
// encoded as Value * 10^Exponent (cosmos LegacyDec coefficient/exponent).
type EpochModelThreshold struct {
	ModelID  string
	Value    int64
	Exponent int32
}

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
//
// Entries with an empty ValidationWeights set are not treated as authoritative:
// refreshModelValidationThresholds can populate the cache at epoch start before
// members are assigned, and a sticky empty cache would make IsActiveParticipant
// permanently return false (breaking confirmation PoC validation).
func (c *EpochGroupDataCache) GetEpochGroupData(ctx context.Context, epochIndex uint64) (*types.EpochGroupData, error) {
	c.mu.RLock()
	if cached, ok := c.epochCache[epochIndex]; ok && len(cached.addressSet) > 0 {
		c.mu.RUnlock()
		return cached.data, nil
	}
	c.mu.RUnlock()

	c.mu.Lock()
	defer c.mu.Unlock()

	// Double-check after acquiring write lock
	if cached, ok := c.epochCache[epochIndex]; ok && len(cached.addressSet) > 0 {
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
	if cached, ok := c.epochCache[epochIndex]; ok && len(cached.addressSet) > 0 {
		_, exists := cached.addressSet[address]
		c.mu.RUnlock()
		return exists, nil
	}
	c.mu.RUnlock()

	// Cache miss or empty (pre-member) entry — fetch / refresh first
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

// GetModelValidationThresholds returns the per-model validation thresholds for
// the epoch's models. It reads the parent epoch group (cached) for the model
// list, then queries each model sub-group for its ModelSnapshot threshold.
// Intended to be called on epoch change (N+1 queries per epoch), so devshardd
// can read thresholds from the long-poll snapshot instead of querying chain.
func (c *EpochGroupDataCache) GetModelValidationThresholds(ctx context.Context, epochIndex uint64) ([]EpochModelThreshold, error) {
	parent, err := c.GetEpochGroupData(ctx, epochIndex)
	if err != nil {
		return nil, err
	}

	qc := c.recorder.NewInferenceQueryClient()
	out := make([]EpochModelThreshold, 0, len(parent.SubGroupModels))
	for _, model := range parent.SubGroupModels {
		resp, err := qc.EpochGroupData(ctx, &types.QueryGetEpochGroupDataRequest{
			EpochIndex: epochIndex,
			ModelId:    model,
		})
		if err != nil {
			return nil, fmt.Errorf("epoch group data epoch=%d model=%s: %w", epochIndex, model, err)
		}
		ms := resp.EpochGroupData.ModelSnapshot
		if ms == nil || ms.ValidationThreshold == nil {
			logging.Warn("model snapshot missing validation threshold", types.Config,
				"epoch", epochIndex, "model", model)
			continue
		}
		out = append(out, EpochModelThreshold{
			ModelID:  model,
			Value:    ms.ValidationThreshold.Value,
			Exponent: ms.ValidationThreshold.Exponent,
		})
	}
	return out, nil
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
