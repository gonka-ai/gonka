package keeper_test

import (
	"testing"

	keeper2 "github.com/productscience/inference/testutil/keeper"
	keeperpkg "github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

// TestUpdateCBStateForBlock_ExcludesHighMissRate verifies that a participant with
// >25% miss rate and ≥4 samples gets excluded during EndBlock.
func TestUpdateCBStateForBlock_ExcludesHighMissRate(t *testing.T) {
	k, ctx := keeper2.InferenceKeeper(t)

	// 1 hit, 3 misses = 75% miss rate — above 25% threshold, total=4 ≥ min_samples
	participant := types.Participant{
		Index:   cbAddr1,
		Address: cbAddr1,
		Status:  types.ParticipantStatus_ACTIVE,
		CurrentEpochStats: &types.CurrentEpochStats{
			InferenceCount: 1,
			MissedRequests: 3,
		},
	}
	err := k.SetParticipant(ctx, participant)
	require.NoError(t, err)

	k.UpdateCBStateForBlock(ctx, ctx.BlockHeight())

	entry := k.GetCBEntry(ctx, cbAddr1)
	require.Equal(t, keeperpkg.CBStateExcluded, entry.State, "node with >25% miss rate and ≥4 samples should be excluded")
}

// TestUpdateCBStateForBlock_PromotesExpiredExclusion verifies that an EXCLUDED entry
// with an expired cooldown gets promoted to PROBE state in EndBlock.
func TestUpdateCBStateForBlock_PromotesExpiredExclusion(t *testing.T) {
	k, ctx := keeper2.InferenceKeeper(t)

	excludedAtBlock := int64(100)
	cooldownBlocks := int64(50)
	k.SetCBEntry(ctx, keeperpkg.CircuitBreakerEntry{
		Address:         cbAddr1,
		State:           keeperpkg.CBStateExcluded,
		ExcludedAtBlock: excludedAtBlock,
		CooldownBlocks:  cooldownBlocks,
	})

	// blockHeight >= excludedAtBlock + cooldownBlocks → cooldown expired
	blockHeight := excludedAtBlock + cooldownBlocks + 1

	k.UpdateCBStateForBlock(ctx, blockHeight)

	entry := k.GetCBEntry(ctx, cbAddr1)
	require.Equal(t, keeperpkg.CBStateProbe, entry.State, "EXCLUDED entry with expired cooldown should be promoted to PROBE")
}

// TestUpdateCBStateForBlock_SkipsLowMissRate verifies that a participant below the
// miss-rate threshold stays HEALTHY after EndBlock.
func TestUpdateCBStateForBlock_SkipsLowMissRate(t *testing.T) {
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

	k.UpdateCBStateForBlock(ctx, ctx.BlockHeight())

	entry := k.GetCBEntry(ctx, cbAddr1)
	require.Equal(t, keeperpkg.CBStateHealthy, entry.State, "node below miss-rate threshold should remain HEALTHY")
}

// TestUpdateCBStateForBlock_SkipsAlreadyExcluded verifies that a node already in
// EXCLUDED state is not re-excluded by the miss-rate pass.
func TestUpdateCBStateForBlock_SkipsAlreadyExcluded(t *testing.T) {
	k, ctx := keeper2.InferenceKeeper(t)

	// Set node to EXCLUDED state with a non-expired cooldown
	excludedAtBlock := int64(100)
	k.SetCBEntry(ctx, keeperpkg.CircuitBreakerEntry{
		Address:         cbAddr1,
		State:           keeperpkg.CBStateExcluded,
		ExcludedAtBlock: excludedAtBlock,
		CooldownBlocks:  keeperpkg.DefaultCBMaxCooldownBlocks,
		ProbeAttempts:   1,
	})

	// Also set a participant with high miss rate
	participant := types.Participant{
		Index:   cbAddr1,
		Address: cbAddr1,
		Status:  types.ParticipantStatus_ACTIVE,
		CurrentEpochStats: &types.CurrentEpochStats{
			InferenceCount: 1,
			MissedRequests: 3,
		},
	}
	err := k.SetParticipant(ctx, participant)
	require.NoError(t, err)

	// blockHeight within cooldown to avoid promotion
	blockHeight := excludedAtBlock + 10

	k.UpdateCBStateForBlock(ctx, blockHeight)

	entry := k.GetCBEntry(ctx, cbAddr1)
	require.Equal(t, keeperpkg.CBStateExcluded, entry.State, "already-EXCLUDED node should not be re-excluded by miss-rate pass")
	// ProbeAttempts should remain unchanged (no re-exclusion happened)
	require.Equal(t, int32(1), entry.ProbeAttempts, "probe attempts should be unchanged for already-EXCLUDED node")
}

// TestUpdateCBStateForBlock_SkipsProbeNodes verifies that PROBE-state nodes are
// not modified by the miss-rate pass in EndBlock.
func TestUpdateCBStateForBlock_SkipsProbeNodes(t *testing.T) {
	k, ctx := keeper2.InferenceKeeper(t)

	// Set node to PROBE state
	k.SetCBEntry(ctx, keeperpkg.CircuitBreakerEntry{
		Address:        cbAddr1,
		State:          keeperpkg.CBStateProbe,
		CooldownBlocks: keeperpkg.DefaultCBInitialCooldownBlocks,
	})

	// Participant with high miss rate — should be ignored because node is in PROBE
	participant := types.Participant{
		Index:   cbAddr1,
		Address: cbAddr1,
		Status:  types.ParticipantStatus_ACTIVE,
		CurrentEpochStats: &types.CurrentEpochStats{
			InferenceCount: 1,
			MissedRequests: 3,
		},
	}
	err := k.SetParticipant(ctx, participant)
	require.NoError(t, err)

	k.UpdateCBStateForBlock(ctx, ctx.BlockHeight())

	entry := k.GetCBEntry(ctx, cbAddr1)
	require.Equal(t, keeperpkg.CBStateProbe, entry.State, "PROBE node should not be modified by miss-rate pass")
}
