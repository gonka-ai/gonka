package internal

import (
	"common/logging"
	"context"
	"decentralized-api/cosmosclient"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/productscience/inference/x/inference/types"
	"golang.org/x/sync/singleflight"
)

// EpochModelThreshold is a per-model inference validation threshold for an epoch,
// encoded as Value * 10^Exponent (cosmos LegacyDec coefficient/exponent).
type EpochModelThreshold struct {
	ModelID  string
	Value    int64
	Exponent int32
}

const (
	maxCachedEpochs                = 8
	epochGroupQueryTimeout         = 15 * time.Second
	maxConcurrentEpochGroupQueries = 4
)

type cachedEpochData struct {
	data       *types.EpochGroupData
	addressSet map[string]struct{} // O(1) lookup for active participants
}

type EpochGroupDataCache struct {
	mu sync.RWMutex

	// Legacy single-epoch cache for GetCurrentEpochGroupData
	cachedEpochIndex uint64
	cachedGroupData  *types.EpochGroupData

	// Multi-epoch cache for GetEpochGroupData (newest maxCachedEpochs epochs)
	epochCache map[uint64]*cachedEpochData

	currentGroupFlight singleflight.Group
	epochGroupFlight   singleflight.Group
	querySlots         chan struct{}
	queryTimeout       time.Duration

	recorder cosmosclient.CosmosMessageClient
}

func NewEpochGroupDataCache(recorder cosmosclient.CosmosMessageClient) *EpochGroupDataCache {
	return &EpochGroupDataCache{
		recorder:     recorder,
		epochCache:   make(map[uint64]*cachedEpochData),
		querySlots:   make(chan struct{}, maxConcurrentEpochGroupQueries),
		queryTimeout: epochGroupQueryTimeout,
	}
}

func boundedEpochQuery[T any](ctx context.Context, slots chan struct{}, query func(context.Context) (T, error)) (T, error) {
	var zero T
	select {
	case slots <- struct{}{}:
	case <-ctx.Done():
		return zero, ctx.Err()
	}

	type queryResult struct {
		value T
		err   error
	}
	done := make(chan queryResult, 1)
	go func() {
		defer func() { <-slots }()
		value, err := query(ctx)
		done <- queryResult{value, err}
	}()

	select {
	case res := <-done:
		return res.value, res.err
	case <-ctx.Done():
		return zero, ctx.Err()
	}
}

func (c *EpochGroupDataCache) GetCurrentEpochGroupData(currentEpochIndex uint64) (*types.EpochGroupData, error) {
	if data := c.cachedCurrentEpochGroupData(currentEpochIndex); data != nil {
		return data, nil
	}

	key := strconv.FormatUint(currentEpochIndex, 10)
	result, err, _ := c.currentGroupFlight.Do(key, func() (interface{}, error) {
		return c.fetchCurrentEpochGroupData(currentEpochIndex)
	})
	if err != nil {
		return nil, err
	}
	return result.(*types.EpochGroupData), nil
}

func (c *EpochGroupDataCache) cachedCurrentEpochGroupData(currentEpochIndex uint64) *types.EpochGroupData {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.cachedGroupData != nil && c.cachedEpochIndex == currentEpochIndex {
		return c.cachedGroupData
	}
	return nil
}

func (c *EpochGroupDataCache) fetchCurrentEpochGroupData(currentEpochIndex uint64) (*types.EpochGroupData, error) {
	if data := c.cachedCurrentEpochGroupData(currentEpochIndex); data != nil {
		return data, nil
	}

	logging.Info("Fetching new epoch group data", types.Config, "currentEpochIndex", currentEpochIndex)

	ctx, cancel := context.WithTimeout(context.Background(), c.queryTimeout)
	defer cancel()
	queryClient := c.recorder.NewInferenceQueryClient()
	req := &types.QueryCurrentEpochGroupDataRequest{}
	resp, err := boundedEpochQuery(ctx, c.querySlots, func(queryCtx context.Context) (*types.QueryCurrentEpochGroupDataResponse, error) {
		return queryClient.CurrentEpochGroupData(queryCtx, req)
	})
	if err != nil {
		logging.Warn("Failed to query current epoch group data", types.Config, "error", err)
		return nil, err
	}

	data := &resp.EpochGroupData
	c.mu.Lock()
	if c.cachedGroupData == nil || currentEpochIndex >= c.cachedEpochIndex {
		c.cachedEpochIndex = currentEpochIndex
		c.cachedGroupData = data
	}
	c.mu.Unlock()

	logging.Info("Updated epoch group data cache", types.Config,
		"epochIndex", currentEpochIndex,
		"validationWeights", len(resp.EpochGroupData.ValidationWeights))

	return data, nil
}

// GetEpochGroupData returns epoch group data for specific epoch.
// Uses cache, queries chain only on cache miss. Keeps the newest maxCachedEpochs epochs.
//
// Entries with an empty ValidationWeights set are not treated as authoritative:
// refreshModelValidationThresholds can populate the cache at epoch start before
// members are assigned, and a sticky empty cache would make IsActiveParticipant
// permanently return false (breaking confirmation PoC validation).
func (c *EpochGroupDataCache) GetEpochGroupData(ctx context.Context, epochIndex uint64) (*types.EpochGroupData, error) {
	if data := c.cachedEpochGroupData(epochIndex); data != nil {
		return data, nil
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	key := strconv.FormatUint(epochIndex, 10)
	queryCtx := context.WithoutCancel(ctx)
	result := c.epochGroupFlight.DoChan(key, func() (interface{}, error) {
		return c.fetchEpochGroupData(queryCtx, epochIndex)
	})

	select {
	case res := <-result:
		if res.Err != nil {
			return nil, res.Err
		}
		return res.Val.(*types.EpochGroupData), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (c *EpochGroupDataCache) cachedEpochGroupData(epochIndex uint64) *types.EpochGroupData {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if cached, ok := c.epochCache[epochIndex]; ok && len(cached.addressSet) > 0 {
		return cached.data
	}
	return nil
}

func (c *EpochGroupDataCache) fetchEpochGroupData(ctx context.Context, epochIndex uint64) (*types.EpochGroupData, error) {
	if data := c.cachedEpochGroupData(epochIndex); data != nil {
		return data, nil
	}

	logging.Debug("Fetching epoch group data", types.Config, "epochIndex", epochIndex)

	queryCtx, cancel := context.WithTimeout(ctx, c.queryTimeout)
	defer cancel()
	queryClient := c.recorder.NewInferenceQueryClient()
	resp, err := boundedEpochQuery(queryCtx, c.querySlots, func(boundedCtx context.Context) (*types.QueryGetEpochGroupDataResponse, error) {
		return queryClient.EpochGroupData(boundedCtx, &types.QueryGetEpochGroupDataRequest{
			EpochIndex: epochIndex,
		})
	})
	if err != nil {
		return nil, err
	}

	addressSet := make(map[string]struct{}, len(resp.EpochGroupData.ValidationWeights))
	for _, vw := range resp.EpochGroupData.ValidationWeights {
		addressSet[vw.MemberAddress] = struct{}{}
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	c.epochCache[epochIndex] = &cachedEpochData{
		data:       &resp.EpochGroupData,
		addressSet: addressSet,
	}
	c.pruneToCapacity()

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
	data, err := c.GetEpochGroupData(ctx, epochIndex)
	if err != nil {
		return false, err
	}

	for _, vw := range data.ValidationWeights {
		if vw.MemberAddress == address {
			return true, nil
		}
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

func (c *EpochGroupDataCache) pruneToCapacity() {
	for len(c.epochCache) > maxCachedEpochs {
		var oldest uint64
		first := true
		for epochId := range c.epochCache {
			if first || epochId < oldest {
				oldest = epochId
				first = false
			}
		}
		delete(c.epochCache, oldest)
		logging.Debug("Pruned old epoch from cache", types.Config, "epochId", oldest)
	}
}
