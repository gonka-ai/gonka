package keeper_test

import (
	"testing"

	"github.com/cosmos/cosmos-sdk/x/group"
	"github.com/productscience/inference/testutil"
	keeper2 "github.com/productscience/inference/testutil/keeper"
	keeperpkg "github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

// cbAddr1 and cbAddr2 are valid node addresses used throughout the circuit breaker tests.
const (
	cbAddr1 = testutil.Executor
	cbAddr2 = testutil.Executor2
)

// TestCBStateDefaultsToHealthy verifies that GetCBEntry returns a healthy entry
// for an address with no stored state.
func TestCBStateDefaultsToHealthy(t *testing.T) {
	k, ctx := keeper2.InferenceKeeper(t)
	entry := k.GetCBEntry(ctx, cbAddr1)
	require.Equal(t, keeperpkg.CBStateHealthy, entry.State)
	require.Equal(t, cbAddr1, entry.Address)
}

// TestCBSetAndGet verifies round-trip persistence of a CB entry.
func TestCBSetAndGet(t *testing.T) {
	k, ctx := keeper2.InferenceKeeper(t)

	entry := keeperpkg.CircuitBreakerEntry{
		Address:         cbAddr1,
		State:           keeperpkg.CBStateExcluded,
		ExcludedAtBlock: 100,
		CooldownBlocks:  keeperpkg.DefaultCBInitialCooldownBlocks,
		ProbeAttempts:   0,
	}
	k.SetCBEntry(ctx, entry)

	got := k.GetCBEntry(ctx, cbAddr1)
	require.Equal(t, keeperpkg.CBStateExcluded, got.State)
	require.Equal(t, int64(100), got.ExcludedAtBlock)
	require.Equal(t, keeperpkg.DefaultCBInitialCooldownBlocks, got.CooldownBlocks)
}

// TestExcludeCBEntry verifies initial exclusion sets state and cooldown correctly.
func TestExcludeCBEntry(t *testing.T) {
	k, ctx := keeper2.InferenceKeeper(t)

	k.ExcludeCBEntry(ctx, cbAddr1, 200, false)

	entry := k.GetCBEntry(ctx, cbAddr1)
	require.Equal(t, keeperpkg.CBStateExcluded, entry.State)
	require.Equal(t, int64(200), entry.ExcludedAtBlock)
	require.Equal(t, keeperpkg.DefaultCBInitialCooldownBlocks, entry.CooldownBlocks)
	require.Equal(t, int32(0), entry.ProbeAttempts)
}

// TestExcludeDoublesCooldownOnReExclusion verifies exponential backoff behaviour.
func TestExcludeDoublesCooldownOnReExclusion(t *testing.T) {
	k, ctx := keeper2.InferenceKeeper(t)

	// Initial exclusion
	k.ExcludeCBEntry(ctx, cbAddr1, 100, false)
	entry := k.GetCBEntry(ctx, cbAddr1)
	require.Equal(t, keeperpkg.DefaultCBInitialCooldownBlocks, entry.CooldownBlocks)

	// First re-exclusion — cooldown should double
	k.ExcludeCBEntry(ctx, cbAddr1, 200, true)
	entry = k.GetCBEntry(ctx, cbAddr1)
	require.Equal(t, keeperpkg.DefaultCBInitialCooldownBlocks*2, entry.CooldownBlocks)
	require.Equal(t, int32(1), entry.ProbeAttempts)

	// Second re-exclusion — cooldown should double again
	k.ExcludeCBEntry(ctx, cbAddr1, 300, true)
	entry = k.GetCBEntry(ctx, cbAddr1)
	require.Equal(t, keeperpkg.DefaultCBInitialCooldownBlocks*4, entry.CooldownBlocks)
	require.Equal(t, int32(2), entry.ProbeAttempts)
}

// TestExcludeCapsCooldownAtMax verifies the max cooldown cap is enforced.
func TestExcludeCapsCooldownAtMax(t *testing.T) {
	k, ctx := keeper2.InferenceKeeper(t)

	// Set a large cooldown before re-excluding to trigger the cap.
	entry := keeperpkg.CircuitBreakerEntry{
		Address:         cbAddr1,
		State:           keeperpkg.CBStateProbe,
		ExcludedAtBlock: 100,
		CooldownBlocks:  keeperpkg.DefaultCBMaxCooldownBlocks - 1,
	}
	k.SetCBEntry(ctx, entry)

	k.ExcludeCBEntry(ctx, cbAddr1, 200, true)
	got := k.GetCBEntry(ctx, cbAddr1)
	require.Equal(t, keeperpkg.DefaultCBMaxCooldownBlocks, got.CooldownBlocks)
}

// TestPromoteCBEntryToProbe verifies state transition EXCLUDED → PROBE.
func TestPromoteCBEntryToProbe(t *testing.T) {
	k, ctx := keeper2.InferenceKeeper(t)

	k.ExcludeCBEntry(ctx, cbAddr1, 100, false)
	k.PromoteCBEntryToProbe(ctx, cbAddr1, 150)

	entry := k.GetCBEntry(ctx, cbAddr1)
	require.Equal(t, keeperpkg.CBStateProbe, entry.State)
}

// TestRecordCBResult_ProbeSuccess verifies PROBE → HEALTHY on success.
func TestRecordCBResult_ProbeSuccess(t *testing.T) {
	k, ctx := keeper2.InferenceKeeper(t)

	// Set node to PROBE state
	k.ExcludeCBEntry(ctx, cbAddr1, 100, false)
	k.PromoteCBEntryToProbe(ctx, cbAddr1, 150)

	k.RecordCBResult(ctx, cbAddr1, 155, true)

	// Entry is kept in the store with State=HEALTHY, LastRestoredBlock set, and
	// ProbeRestored=true so that EndBlock can apply the one-block grace period.
	entry := k.GetCBEntry(ctx, cbAddr1)
	require.Equal(t, keeperpkg.CBStateHealthy, entry.State)
	require.Equal(t, int64(155), entry.LastRestoredBlock, "LastRestoredBlock should record the recovery block")
	require.True(t, entry.ProbeRestored, "ProbeRestored should be true after probe success")
}

// TestRecordCBResult_ProbeFailure verifies PROBE → EXCLUDED (doubled cooldown) on miss.
func TestRecordCBResult_ProbeFailure(t *testing.T) {
	k, ctx := keeper2.InferenceKeeper(t)

	k.ExcludeCBEntry(ctx, cbAddr1, 100, false)
	k.PromoteCBEntryToProbe(ctx, cbAddr1, 150)

	k.RecordCBResult(ctx, cbAddr1, 155, false)

	entry := k.GetCBEntry(ctx, cbAddr1)
	require.Equal(t, keeperpkg.CBStateExcluded, entry.State)
	require.Equal(t, keeperpkg.DefaultCBInitialCooldownBlocks*2, entry.CooldownBlocks)
	require.Equal(t, int32(1), entry.ProbeAttempts)
}

// TestRecordCBResult_HealthyNodeNoOp verifies RecordCBResult is a no-op for healthy nodes.
func TestRecordCBResult_HealthyNodeNoOp(t *testing.T) {
	k, ctx := keeper2.InferenceKeeper(t)

	// Healthy node: result should not change anything
	k.RecordCBResult(ctx, cbAddr1, 100, false)
	entry := k.GetCBEntry(ctx, cbAddr1)
	require.Equal(t, keeperpkg.CBStateHealthy, entry.State)
}

// TestClearAllCBState verifies all entries are removed on epoch boundary.
func TestClearAllCBState(t *testing.T) {
	k, ctx := keeper2.InferenceKeeper(t)

	k.ExcludeCBEntry(ctx, cbAddr1, 100, false)
	k.ExcludeCBEntry(ctx, cbAddr2, 100, false)

	require.Equal(t, keeperpkg.CBStateExcluded, k.GetCBEntry(ctx, cbAddr1).State)
	require.Equal(t, keeperpkg.CBStateExcluded, k.GetCBEntry(ctx, cbAddr2).State)

	k.ClearAllCBState(ctx)

	require.Equal(t, keeperpkg.CBStateHealthy, k.GetCBEntry(ctx, cbAddr1).State)
	require.Equal(t, keeperpkg.CBStateHealthy, k.GetCBEntry(ctx, cbAddr2).State)
}

// TestHealthFilterExcludesHighMissRate verifies that a member with >25% miss rate
// and ≥4 samples is excluded from the filtered list.
// Uses two members so the safety fallback (return-all-when-all-excluded) doesn't fire.
func TestHealthFilterExcludesHighMissRate(t *testing.T) {
	k, ctx := keeper2.InferenceKeeper(t)

	// cbAddr1: 3 hits, 4 misses = 57% miss rate — above 25% threshold, above min 4 samples
	unhealthy := types.Participant{
		Index:   cbAddr1,
		Address: cbAddr1,
		Status:  types.ParticipantStatus_ACTIVE,
		CurrentEpochStats: &types.CurrentEpochStats{
			InferenceCount: 3,
			MissedRequests: 4,
		},
	}
	err := k.SetParticipant(ctx, unhealthy)
	require.NoError(t, err)

	// cbAddr2: healthy node to prevent the safety fallback from returning all members
	healthy := types.Participant{
		Index:   cbAddr2,
		Address: cbAddr2,
		Status:  types.ParticipantStatus_ACTIVE,
		CurrentEpochStats: &types.CurrentEpochStats{
			InferenceCount: 9,
			MissedRequests: 1,
		},
	}
	err = k.SetParticipant(ctx, healthy)
	require.NoError(t, err)

	filter := k.CreateHealthFilterFnForTest(ctx, ctx.BlockHeight())
	members := makeCBMockMembers(cbAddr1, cbAddr2)
	result := filter(members)

	// cbAddr1 should be excluded; cbAddr2 should remain
	require.Len(t, result, 1, "only the healthy node should survive the filter")
	require.Equal(t, cbAddr2, result[0].Member.Address, "node with >25% miss rate should be excluded")

	// Filter is now read-only: CB state must remain Healthy (EndBlock handles state transition)
	entry := k.GetCBEntry(ctx, cbAddr1)
	require.Equal(t, keeperpkg.CBStateHealthy, entry.State)
}

// TestHealthFilterIncludesHealthyNode verifies healthy nodes pass the filter.
func TestHealthFilterIncludesHealthyNode(t *testing.T) {
	k, ctx := keeper2.InferenceKeeper(t)

	// 9 hits, 1 miss = 10% miss rate — below 25% threshold
	participant := types.Participant{
		Index:   cbAddr1,
		Address: cbAddr1,
		Status:  types.ParticipantStatus_ACTIVE,
		CurrentEpochStats: &types.CurrentEpochStats{
			InferenceCount: 9,
			MissedRequests: 1,
		},
	}
	err := k.SetParticipant(ctx, participant)
	require.NoError(t, err)

	filter := k.CreateHealthFilterFnForTest(ctx, ctx.BlockHeight())
	members := makeCBMockMembers(cbAddr1)
	result := filter(members)

	require.Len(t, result, 1, "node with low miss rate should be included")
}

// TestHealthFilterBelowMinSamples verifies nodes with fewer than min samples are
// not excluded even if the current rate appears high.
func TestHealthFilterBelowMinSamples(t *testing.T) {
	k, ctx := keeper2.InferenceKeeper(t)

	// 1 hit, 2 misses = 66% miss rate, but total=3 < min_samples=4
	participant := types.Participant{
		Index:   cbAddr1,
		Address: cbAddr1,
		Status:  types.ParticipantStatus_ACTIVE,
		CurrentEpochStats: &types.CurrentEpochStats{
			InferenceCount: 1,
			MissedRequests: 2,
		},
	}
	err := k.SetParticipant(ctx, participant)
	require.NoError(t, err)

	filter := k.CreateHealthFilterFnForTest(ctx, ctx.BlockHeight())
	members := makeCBMockMembers(cbAddr1)
	result := filter(members)

	require.Len(t, result, 1, "node with too few samples should not be excluded")
}

// TestHealthFilterProbeNodePromotedOnCooldownExpiry verifies that an EXCLUDED node
// is promoted to PROBE when its cooldown expires, and included in the filtered list.
func TestHealthFilterProbeNodePromotedOnCooldownExpiry(t *testing.T) {
	k, ctx := keeper2.InferenceKeeper(t)

	excludedAtBlock := int64(100)
	cooldownBlocks := int64(50)
	k.SetCBEntry(ctx, keeperpkg.CircuitBreakerEntry{
		Address:         cbAddr1,
		State:           keeperpkg.CBStateExcluded,
		ExcludedAtBlock: excludedAtBlock,
		CooldownBlocks:  cooldownBlocks,
	})

	// Advance the block height past the cooldown
	blockHeight := excludedAtBlock + cooldownBlocks + 1

	filter := k.CreateHealthFilterFnForTest(ctx, blockHeight)
	members := makeCBMockMembers(cbAddr1)
	result := filter(members)

	require.Len(t, result, 1, "excluded node with expired cooldown should be included (probe pending EndBlock)")

	// Filter is now read-only: CB state must remain Excluded (EndBlock handles EXCLUDED → PROBE transition)
	entry := k.GetCBEntry(ctx, cbAddr1)
	require.Equal(t, keeperpkg.CBStateExcluded, entry.State)
}

// TestHealthFilterExcludedNodeStillInCooldown verifies that an excluded node
// within its cooldown window remains excluded.
// Uses two members so the safety fallback (return-all-when-all-excluded) doesn't fire.
func TestHealthFilterExcludedNodeStillInCooldown(t *testing.T) {
	k, ctx := keeper2.InferenceKeeper(t)

	excludedAtBlock := int64(100)
	cooldownBlocks := int64(50)
	k.SetCBEntry(ctx, keeperpkg.CircuitBreakerEntry{
		Address:         cbAddr1,
		State:           keeperpkg.CBStateExcluded,
		ExcludedAtBlock: excludedAtBlock,
		CooldownBlocks:  cooldownBlocks,
	})

	// cbAddr2: healthy node to prevent the safety fallback from returning all members
	healthy := types.Participant{
		Index:   cbAddr2,
		Address: cbAddr2,
		Status:  types.ParticipantStatus_ACTIVE,
		CurrentEpochStats: &types.CurrentEpochStats{
			InferenceCount: 9,
			MissedRequests: 1,
		},
	}
	err := k.SetParticipant(ctx, healthy)
	require.NoError(t, err)

	// Block height still within cooldown
	blockHeight := excludedAtBlock + 10

	filter := k.CreateHealthFilterFnForTest(ctx, blockHeight)
	members := makeCBMockMembers(cbAddr1, cbAddr2)
	result := filter(members)

	// cbAddr1 (excluded, in cooldown) should be filtered out; cbAddr2 should remain
	require.Len(t, result, 1, "only the healthy node should survive the filter")
	require.Equal(t, cbAddr2, result[0].Member.Address, "excluded node within cooldown should remain excluded")
}

// TestHealthFilterFallbackAllDegraded verifies the safety fallback: if all nodes
// are excluded, the filter returns the original list to prevent empty pool crash.
func TestHealthFilterFallbackAllDegraded(t *testing.T) {
	k, ctx := keeper2.InferenceKeeper(t)

	// Both nodes excluded, cooldown not expired
	for _, addr := range []string{cbAddr1, cbAddr2} {
		k.SetCBEntry(ctx, keeperpkg.CircuitBreakerEntry{
			Address:         addr,
			State:           keeperpkg.CBStateExcluded,
			ExcludedAtBlock: 100,
			CooldownBlocks:  keeperpkg.DefaultCBMaxCooldownBlocks,
		})
	}

	filter := k.CreateHealthFilterFnForTest(ctx, 110)
	members := makeCBMockMembers(cbAddr1, cbAddr2)
	result := filter(members)

	require.Len(t, result, 2, "all-degraded fallback should return full member list")
}

// TestHealthFilterProbeNodeIncluded verifies a PROBE state node is included in the filter.
func TestHealthFilterProbeNodeIncluded(t *testing.T) {
	k, ctx := keeper2.InferenceKeeper(t)

	k.ExcludeCBEntry(ctx, cbAddr1, 100, false)
	k.PromoteCBEntryToProbe(ctx, cbAddr1, 150)

	filter := k.CreateHealthFilterFnForTest(ctx, 160)
	members := makeCBMockMembers(cbAddr1)
	result := filter(members)

	require.Len(t, result, 1, "PROBE node should be included in filter result")
}

// makeCBMockMembers creates a slice of mock GroupMember pointers for the given addresses.
func makeCBMockMembers(addresses ...string) []*group.GroupMember {
	out := make([]*group.GroupMember, 0, len(addresses))
	for _, addr := range addresses {
		out = append(out, &group.GroupMember{
			Member: &group.Member{
				Address: addr,
				Weight:  "1",
			},
		})
	}
	return out
}
