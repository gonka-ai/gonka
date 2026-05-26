package devshard

import (
	"context"
	"fmt"
	"sync"
	"time"

	"devshard/bridge"
)

const ValidationThresholdCacheTTL = 10 * time.Minute

type validationThresholdCacheKey struct {
	escrowID string
	modelID  string
}

type validationThresholdCacheEntry struct {
	epochID   uint64
	threshold *bridge.Decimal
	expiresAt time.Time
}

// ValidationThresholdResolver loads the epoch-pinned model threshold used by
// mainnet validation and caches it for the lifetime of a devshard escrow.
type ValidationThresholdResolver struct {
	bridge bridge.MainnetBridge
	ttl    time.Duration

	mu    sync.Mutex
	cache map[validationThresholdCacheKey]validationThresholdCacheEntry
}

func NewValidationThresholdResolver(br bridge.MainnetBridge, ttl time.Duration) *ValidationThresholdResolver {
	if ttl <= 0 {
		ttl = ValidationThresholdCacheTTL
	}
	return &ValidationThresholdResolver{
		bridge: br,
		ttl:    ttl,
		cache:  make(map[validationThresholdCacheKey]validationThresholdCacheEntry),
	}
}

func (r *ValidationThresholdResolver) Resolve(
	ctx context.Context,
	escrowID string,
	epochID uint64,
	modelID string,
) (*bridge.Decimal, error) {
	if r == nil {
		return nil, fmt.Errorf("validation threshold resolver is nil")
	}

	key := validationThresholdCacheKey{escrowID: escrowID, modelID: modelID}
	now := time.Now()

	r.mu.Lock()
	if entry, ok := r.cache[key]; ok && entry.epochID == epochID && now.Before(entry.expiresAt) {
		threshold := cloneDecimal(entry.threshold)
		r.mu.Unlock()
		return threshold, nil
	}
	r.mu.Unlock()

	if r.bridge == nil {
		return nil, fmt.Errorf("validation threshold resolver has no bridge")
	}

	threshold, err := r.bridge.GetValidationThreshold(epochID, modelID)
	if err != nil {
		return nil, err
	}
	if threshold == nil {
		return nil, fmt.Errorf("validation threshold missing for epoch %d model %s", epochID, modelID)
	}

	r.mu.Lock()
	r.cache[key] = validationThresholdCacheEntry{
		epochID:   epochID,
		threshold: cloneDecimal(threshold),
		expiresAt: now.Add(r.ttl),
	}
	r.mu.Unlock()

	return cloneDecimal(threshold), nil
}

func cloneDecimal(d *bridge.Decimal) *bridge.Decimal {
	if d == nil {
		return nil
	}
	return &bridge.Decimal{Value: d.Value, Exponent: d.Exponent}
}
