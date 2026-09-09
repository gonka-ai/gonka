package inference

import (
	"context"
	"fmt"
	"sync"
	"time"

	commonvalidation "common/validation"
	commrc "common/runtimeconfig"
	"devshard/bridge"
)

// thresholdSnapshotSource is the subset of the runtime-params provider used to
// read per-model validation thresholds carried on the long-poll snapshot.
type thresholdSnapshotSource interface {
	Snapshot() commrc.Snapshot
}

// ValidationThresholdResolver returns the per-model similarity pass threshold
// for an (epoch, model). It prefers the long-poll RuntimeConfig snapshot served
// by decentralized-api and falls back to a direct chain query on miss.
type ValidationThresholdResolver interface {
	Resolve(ctx context.Context, epochID uint64, model string) (float64, error)
}

const validationThresholdCacheTTL = 10 * time.Minute

type thresholdCacheKey struct {
	epochID uint64
	model   string
}

type thresholdCacheEntry struct {
	value     float64
	expiresAt time.Time
}

// snapshotThresholdResolver reads thresholds from the runtime-config snapshot
// (no chain query in the hot path) and falls back to the mainnet bridge, caching
// fallback results per (epoch, model) so a missing snapshot entry costs at most
// one chain query per epoch+model.
type snapshotThresholdResolver struct {
	source thresholdSnapshotSource
	bridge bridge.MainnetBridge
	ttl    time.Duration

	mu    sync.Mutex
	cache map[thresholdCacheKey]thresholdCacheEntry
}

// NewValidationThresholdResolver builds a resolver. source may be nil (always
// use the bridge); bridge may be nil (snapshot only, error on miss).
func NewValidationThresholdResolver(source thresholdSnapshotSource, br bridge.MainnetBridge) ValidationThresholdResolver {
	return &snapshotThresholdResolver{
		source: source,
		bridge: br,
		ttl:    validationThresholdCacheTTL,
		cache:  make(map[thresholdCacheKey]thresholdCacheEntry),
	}
}

func (r *snapshotThresholdResolver) Resolve(ctx context.Context, epochID uint64, model string) (float64, error) {
	if r == nil {
		return 0, fmt.Errorf("validation threshold resolver is nil")
	}

	// Prefer the long-poll snapshot, but only when it is pinned to the requested
	// epoch — thresholds are per-epoch+model, so a stale-epoch snapshot must not
	// be trusted; fall through to the direct chain query instead.
	if r.source != nil {
		snap := r.source.Snapshot()
		if snap.CurrentEpochID == epochID {
			for _, t := range snap.ModelValidationThresholds {
				if t.ModelID == model {
					return commonvalidation.DecimalToFloat(t.Value, t.Exponent), nil
				}
			}
		}
	}

	return r.resolveFromBridge(epochID, model)
}

func (r *snapshotThresholdResolver) resolveFromBridge(epochID uint64, model string) (float64, error) {
	key := thresholdCacheKey{epochID: epochID, model: model}
	now := time.Now()

	r.mu.Lock()
	if entry, ok := r.cache[key]; ok && now.Before(entry.expiresAt) {
		value := entry.value
		r.mu.Unlock()
		return value, nil
	}
	r.mu.Unlock()

	if r.bridge == nil {
		return 0, fmt.Errorf("validation threshold missing for epoch %d model %s and no chain fallback", epochID, model)
	}

	dec, err := r.bridge.GetValidationThreshold(epochID, model)
	if err != nil {
		return 0, fmt.Errorf("fetch validation threshold epoch=%d model=%s: %w", epochID, model, err)
	}
	if dec == nil {
		return 0, fmt.Errorf("validation threshold missing for epoch %d model %s", epochID, model)
	}
	value := commonvalidation.DecimalToFloat(dec.Value, dec.Exponent)

	r.mu.Lock()
	r.cache[key] = thresholdCacheEntry{value: value, expiresAt: now.Add(r.ttl)}
	r.mu.Unlock()

	return value, nil
}
