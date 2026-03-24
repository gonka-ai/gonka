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
// These are intentionally kept as Go constants (not proto params) for the initial implementation.
// They can be promoted to ValidationParams fields once the proto pipeline is updated.
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

// CircuitBreakerEntry holds the per-node state managed by the fast circuit breaker.
type CircuitBreakerEntry struct {
	Address        string  `json:"address"`
	State          CBState `json:"state"`
	ExcludedAtBlock int64  `json:"excluded_at_block"`
	CooldownBlocks int64   `json:"cooldown_blocks"`
	ProbeAttempts  int32   `json:"probe_attempts"`
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

	newCooldown := entry.CooldownBlocks
	if newCooldown <= 0 {
		newCooldown = DefaultCBInitialCooldownBlocks
	}

	if reExclusion {
		// Exponential backoff: double the cooldown
		newCooldown *= 2
		if newCooldown > DefaultCBMaxCooldownBlocks {
			newCooldown = DefaultCBMaxCooldownBlocks
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
		// Probe succeeded — restore to healthy, reset cooldown
		k.Logger().Info("CircuitBreaker: probe succeeded, node restored to healthy",
			"address", address, "blockHeight", blockHeight)
		k.DeleteCBEntry(ctx, address)
	} else {
		// Probe failed — re-exclude with doubled cooldown
		k.Logger().Info("CircuitBreaker: probe failed, re-excluding node",
			"address", address, "blockHeight", blockHeight)
		k.ExcludeCBEntry(ctx, address, blockHeight, true)
	}
}
