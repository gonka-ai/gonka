package simulation_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/simulation"
)

// TestEnsureModelsInEpochGroup_TolerantOnMissingEpoch — without an
// EffectiveEpochIndex set, GetCurrentEpochGroup returns
// ErrEffectiveEpochNotFound; the helper must treat that as a no-op
// (returns nil) so unit-test keepers without full InitGenesisEpochGroup
// wiring still let the factories run.
func TestEnsureModelsInEpochGroup_TolerantOnMissingEpoch(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	// No SetEffectiveEpochIndex call — GetCurrentEpochGroup will fail.
	require.NoError(t, simulation.EnsureModelsInEpochGroup(ctx, k))
}

// TestEnsureModelsInEpochGroup_TolerantOnMissingEpochGroupData — index
// is set but no EpochGroupData exists yet; helper must also no-op for
// this branch (ErrEpochGroupDataNotFound).
func TestEnsureModelsInEpochGroup_TolerantOnMissingEpochGroupData(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, 0))
	require.NoError(t, simulation.EnsureModelsInEpochGroup(ctx, k))
}
