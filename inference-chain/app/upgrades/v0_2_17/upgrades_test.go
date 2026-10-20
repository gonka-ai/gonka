package v0_2_17

import (
	"testing"

	keepertest "github.com/productscience/inference/testutil/keeper"
	inferencetypes "github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

// TestUpgradeName pins the future on-chain proposal name. The governance
// proposal and UpgradeName must stay identical or the handler will not run.
func TestUpgradeName(t *testing.T) {
	require.Equal(t, "v0.2.17", UpgradeName)
}

func TestMigrateEpochValidationParams(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.EpochValidationParams = nil
	require.NoError(t, k.SetParams(ctx, params))

	require.NoError(t, migrateEpochValidationParams(ctx, k))

	got, err := k.GetParams(ctx)
	require.NoError(t, err)
	require.Equal(t, inferencetypes.DefaultEpochValidationParams(), got.EpochValidationParams)

	got.EpochValidationParams.MaxValidationsPerMessage = 64
	require.NoError(t, k.SetParams(ctx, got))
	require.NoError(t, migrateEpochValidationParams(ctx, k))
	again, err := k.GetParams(ctx)
	require.NoError(t, err)
	require.Equal(t, uint32(64), again.EpochValidationParams.MaxValidationsPerMessage)
}
