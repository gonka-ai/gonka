package keeper_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
)

// TestActualCostWithinEscrowInvariant exercises the module tripwire that guards the
// "ActualCost <= EscrowAmount" invariant.
func TestActualCostWithinEscrowInvariant(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	inv := keeper.ActualCostWithinEscrowInvariant(k)

	// Empty state is not broken.
	_, broken := inv(ctx)
	require.False(t, broken, "no inferences => invariant holds")

	// Healthy: started inference with ActualCost <= EscrowAmount.
	require.NoError(t, k.SetInference(ctx, types.Inference{
		Index: "ok", InferenceId: "ok", AssignedTo: "x",
		EscrowAmount: 1000, ActualCost: 900,
	}))
	_, broken = inv(ctx)
	require.False(t, broken, "ActualCost <= EscrowAmount => invariant holds")

	// Finish-first transient: StartInference not yet processed (AssignedTo == ""),
	// EscrowAmount == 0. This is benign (consumption clamps to 0) and must be skipped.
	require.NoError(t, k.SetInference(ctx, types.Inference{
		Index: "pending", InferenceId: "pending", AssignedTo: "",
		EscrowAmount: 0, ActualCost: 1_000_000,
	}))
	_, broken = inv(ctx)
	require.False(t, broken, "finish-first transient must not trip the invariant")

	// Violation: a started inference whose ActualCost exceeds the escrow collected.
	require.NoError(t, k.SetInference(ctx, types.Inference{
		Index: "bad", InferenceId: "bad", AssignedTo: "x",
		EscrowAmount: 11_000, ActualCost: 1_000_010_000,
	}))
	msg, broken := inv(ctx)
	require.True(t, broken, "ActualCost > EscrowAmount on a started inference must trip the invariant")
	require.Contains(t, msg, "bad")
}
