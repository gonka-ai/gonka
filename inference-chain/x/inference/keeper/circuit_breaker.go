package keeper

import (
	"context"
	"encoding/json"

	"cosmossdk.io/store/prefix"
	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/productscience/inference/x/inference/types"
)

// CBState represents the health state of a node in the fast circuit breaker.
type CBState int32

const (
	// CBStateHealthy is the normal operating state. Node is included in selection.
	CBStateHealthy CBState = 0
	// CBStateExcluded means the node exceeded the miss-rate threshold and is in cooldown.
	CBStateExcluded CBState = 1
	// CBStateProbe means the cooldown has expired; the node gets one "test slot" inference.
	CBStateProbe CBState = 2
)

// Default circuit breaker parameters.
// These compile-time constants are used as fallback defaults when the governance
// parameters (ValidationParams.CbMissThresholdPct etc.) are not set (zero value).
const (
	// DefaultCBMissThresholdPct is the miss-rate percentage (0–100) above which a node is excluded.
	DefaultCBMissThresholdPct = uint64(25)
	// DefaultCBMinSamples is the minimum number of completed inferences (hits + misses) required
	// before the miss-rate threshold is applied.
	DefaultCBMinSamples = uint64(4)
	// DefaultCBInitialCooldownBlocks is the initial cooldown period before promoting to PROBE.
	// ~5 minutes at 5 s/block.
	DefaultCBInitialCooldownBlocks = int64(50)
	// DefaultCBMaxCooldownBlocks caps exponential backoff.
	// ~50 minutes at 5 s/block.
	DefaultCBMaxCooldownBlocks = int64(500)
)

// cbParams holds the resolved circuit-breaker tuning parameters for a single call.
type cbParams struct {
	MissThresholdPct      uint64
	MinSamples            uint64
	InitialCooldownBlocks int64
	MaxCooldownBlocks     int64
}

// getCBParams reads CB parameters from ValidationParams and falls back to Go
// compile-time defaults for any field that is zero (unset).
func (k Keeper) getCBParams(ctx context.Context) cbParams {
	p := cbParams{
		MissThresholdPct:      DefaultCBMissThresholdPct,
		MinSamples:            DefaultCBMinSamples,
		InitialCooldownBlocks: DefaultCBInitialCooldownBlocks,
		MaxCooldownBlocks:     DefaultCBMaxCooldownBlocks,
	}
	params, err := k.GetParams(ctx)
	if err != nil {
		return p
	}
	if vp := params.ValidationParams; vp != nil {
		if vp.CbMissThresholdPct != 0 {
			p.MissThresholdPct = vp.CbMissThresholdPct
		}
		if vp.CbMinSamples != 0 {
			p.MinSamples = vp.CbMinSamples
		}
		if vp.CbInitialCooldownBlocks != 0 {
			p.InitialCooldownBlocks = vp.CbInitialCooldownBlocks
		}
		if vp.CbMaxCooldownBlocks != 0 {
			p.MaxCooldownBlocks = vp.CbMaxCooldownBlocks
		}
	}
	return p
}

// CircuitBreakerEntry holds the per-node state managed by the fast circuit breaker.
type CircuitBreakerEntry struct {
	Address          string  `json:"address"`
	State            CBState `json:"state"`
	ExcludedAtBlock  int64   `json:"excluded_at_block"`
	CooldownBlocks   int64   `json:"cooldown_blocks"`
	ProbeAttempts    int32   `json:"probe_attempts"`
	// LastRestoredBlock is the block height at which a probe succeeded and the node was
	// restored to HEALTHY. UpdateCBStateForBlock Pass 2 skips nodes where
	// ProbeRestored == true && blockHeight == LastRestoredBlock (one-block grace period).
	LastRestoredBlock int64 `json:"last_restored_block,omitempty"`
	// ProbeRestored is true when the node was just restored from a probe success.
	// This flag gates the grace-period check to avoid false positives when both
	// LastRestoredBlock and blockHeight are zero (e.g. in tests or genesis block).
	ProbeRestored bool `json:"probe_restored,omitempty"`
}

// cbStoreKey returns the raw byte key used to store a CB entry for an address.
func cbStoreKey(address string) []byte {
	return []byte(address)
}

// GetCBEntry retrieves the circuit breaker entry for the given address.
// Returns a default (healthy) entry if none exists.
func (k Keeper) GetCBEntry(ctx context.Context, address string) CircuitBreakerEntry {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, types.KeyPrefix(types.CircuitBreakerStateKey))

	bz := store.Get(cbStoreKey(address))
	if bz == nil {
		return CircuitBreakerEntry{
			Address:        address,
			State:          CBStateHealthy,
			CooldownBlocks: DefaultCBInitialCooldownBlocks,
		}
	}

	var entry CircuitBreakerEntry
	if err := json.Unmarshal(bz, &entry); err != nil {
		k.Logger().Error("GetCBEntry: failed to unmarshal circuit breaker entry",
			"address", address, "error", err)
		return CircuitBreakerEntry{
			Address:        address,
			State:          CBStateHealthy,
			CooldownBlocks: DefaultCBInitialCooldownBlocks,
		}
	}
	return entry
}

// SetCBEntry persists a circuit breaker entry.
func (k Keeper) SetCBEntry(ctx context.Context, entry CircuitBreakerEntry) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, types.KeyPrefix(types.CircuitBreakerStateKey))

	bz, err := json.Marshal(entry)
	if err != nil {
		k.Logger().Error("SetCBEntry: failed to marshal circuit breaker entry",
			"address", entry.Address, "error", err)
		return
	}
	store.Set(cbStoreKey(entry.Address), bz)
}

// DeleteCBEntry removes a circuit breaker entry from the store.
func (k Keeper) DeleteCBEntry(ctx context.Context, address string) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, types.KeyPrefix(types.CircuitBreakerStateKey))
	store.Delete(cbStoreKey(address))
}

// ClearAllCBState removes all circuit breaker entries.
// Called on epoch boundary to allow fresh evaluation in the new epoch.
func (k Keeper) ClearAllCBState(ctx context.Context) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, types.KeyPrefix(types.CircuitBreakerStateKey))

	// Collect all keys first, then delete (cannot delete during iteration).
	iter := store.Iterator(nil, nil)
	var keysToDelete [][]byte
	for ; iter.Valid(); iter.Next() {
		// Copy the key bytes — iter.Key() is only valid until the next call.
		keyCopy := make([]byte, len(iter.Key()))
		copy(keyCopy, iter.Key())
		keysToDelete = append(keysToDelete, keyCopy)
	}
	iter.Close()

	for _, key := range keysToDelete {
		store.Delete(key)
	}
}

// ExcludeCBEntry transitions a node to EXCLUDED state with the given cooldown.
// If the node was already excluded (re-exclusion during probe), the cooldown doubles (capped).
func (k Keeper) ExcludeCBEntry(ctx context.Context, address string, blockHeight int64, reExclusion bool) {
	entry := k.GetCBEntry(ctx, address)
	cbp := k.getCBParams(ctx)

	newCooldown := entry.CooldownBlocks
	if newCooldown <= 0 {
		newCooldown = cbp.InitialCooldownBlocks
	}

	if reExclusion {
		// Exponential backoff: double the cooldown
		newCooldown *= 2
		if newCooldown > cbp.MaxCooldownBlocks {
			newCooldown = cbp.MaxCooldownBlocks
		}
		entry.ProbeAttempts++
	}

	entry.Address = address
	entry.State = CBStateExcluded
	entry.ExcludedAtBlock = blockHeight
	entry.CooldownBlocks = newCooldown

	k.SetCBEntry(ctx, entry)

	k.Logger().Info("CircuitBreaker: node excluded",
		"address", address,
		"blockHeight", blockHeight,
		"cooldownBlocks", newCooldown,
		"reExclusion", reExclusion,
		"probeAttempts", entry.ProbeAttempts)
}

// PromoteCBEntryToProbe transitions a node from EXCLUDED to PROBE state.
func (k Keeper) PromoteCBEntryToProbe(ctx context.Context, address string, blockHeight int64) {
	entry := k.GetCBEntry(ctx, address)
	entry.Address = address
	entry.State = CBStateProbe
	k.SetCBEntry(ctx, entry)

	k.Logger().Info("CircuitBreaker: node promoted to probe",
		"address", address,
		"blockHeight", blockHeight,
		"probeAttempts", entry.ProbeAttempts)
}

// GetAllCBEntries returns all stored circuit breaker entries.
// Used by EndBlock to apply state transitions across the full CB table.
func (k Keeper) GetAllCBEntries(ctx context.Context) []CircuitBreakerEntry {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, types.KeyPrefix(types.CircuitBreakerStateKey))

	iter := store.Iterator(nil, nil)
	defer iter.Close()

	var entries []CircuitBreakerEntry
	for ; iter.Valid(); iter.Next() {
		var entry CircuitBreakerEntry
		if err := json.Unmarshal(iter.Value(), &entry); err != nil {
			k.Logger().Error("GetAllCBEntries: failed to unmarshal entry", "error", err)
			continue
		}
		entries = append(entries, entry)
	}
	return entries
}

// UpdateCBStateForBlock processes circuit breaker state transitions for the current block.
// Must be called from EndBlock (write context only).
//
// It performs two passes:
// 1. Promote any EXCLUDED entries whose cooldown has expired to PROBE state.
// 2. Scan active participants and exclude any HEALTHY nodes that have crossed the
//    miss-rate threshold (read from ValidationParams, falling back to Go constant defaults).
func (k Keeper) UpdateCBStateForBlock(ctx context.Context, blockHeight int64) {
	cbp := k.getCBParams(ctx)

	// Pass 1: EXCLUDED → PROBE promotion for expired cooldowns
	entries := k.GetAllCBEntries(ctx)
	for _, entry := range entries {
		if entry.State == CBStateExcluded {
			if blockHeight >= entry.ExcludedAtBlock+entry.CooldownBlocks {
				k.PromoteCBEntryToProbe(ctx, entry.Address, blockHeight)
				k.Logger().Info("CircuitBreaker: promoted expired entry to probe",
					"address", entry.Address, "blockHeight", blockHeight)
			}
		}
	}

	// Pass 2: HEALTHY → EXCLUDED for high-miss-rate participants
	participants := k.GetAllParticipant(ctx)
	for _, p := range participants {
		if p.CurrentEpochStats == nil {
			continue
		}
		inferenceCount := p.CurrentEpochStats.InferenceCount
		missedRequests := p.CurrentEpochStats.MissedRequests
		total := inferenceCount + missedRequests

		// Only apply threshold if node already has a clean bill of health (not already excluded/probe)
		existing := k.GetCBEntry(ctx, p.Index)
		if existing.State != CBStateHealthy {
			continue
		}

		// Grace period: skip nodes that were just restored to HEALTHY by a probe success
		// in this same block. Without this, EndBlock Pass 2 would immediately re-exclude
		// them based on stale miss-rate stats before any new inference data has arrived.
		// ProbeRestored guards against false positives when LastRestoredBlock and
		// blockHeight are both zero (default value for nodes never in a probe cycle).
		if existing.ProbeRestored && existing.LastRestoredBlock == blockHeight {
			continue
		}

		if total >= cbp.MinSamples && missedRequests*100 > cbp.MissThresholdPct*total {
			k.ExcludeCBEntry(ctx, p.Index, blockHeight, false)
			k.Logger().Info("CircuitBreaker: excluded node via EndBlock miss-rate check",
				"address", p.Index, "inferenceCount", inferenceCount,
				"missedRequests", missedRequests, "blockHeight", blockHeight)
		}
	}
}

// RecordCBResult updates the circuit breaker state after an inference result.
// success=true: node completed inference → restore to HEALTHY.
// success=false: node missed/timed out → re-exclude with doubled cooldown.
//
// This is only meaningful if the node was in PROBE state; for HEALTHY nodes
// the state machine is driven by miss-rate in createHealthFilterFn.
func (k Keeper) RecordCBResult(ctx context.Context, address string, blockHeight int64, success bool) {
	entry := k.GetCBEntry(ctx, address)

	// Only act on PROBE state; HEALTHY nodes are managed by the filter
	if entry.State != CBStateProbe {
		return
	}

	if success {
		// Probe succeeded — restore to HEALTHY and record the block height.
		// We keep the entry (instead of deleting it) so that UpdateCBStateForBlock
		// Pass 2 can detect the same-block grace period via LastRestoredBlock and
		// skip the miss-rate re-exclusion check for this block.
		k.Logger().Info("CircuitBreaker: probe succeeded, node restored to healthy",
			"address", address, "blockHeight", blockHeight)
		entry.State = CBStateHealthy
		entry.LastRestoredBlock = blockHeight
		entry.ProbeRestored = true
		k.SetCBEntry(ctx, entry)
	} else {
		// Probe failed — re-exclude with doubled cooldown
		k.Logger().Info("CircuitBreaker: probe failed, re-excluding node",
			"address", address, "blockHeight", blockHeight)
		k.ExcludeCBEntry(ctx, address, blockHeight, true)
	}
}
