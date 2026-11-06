package v0_2_18

import (
	"testing"

	keepertest "github.com/productscience/inference/testutil/keeper"
	inferencetypes "github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

// TestUpgradeName pins the future on-chain proposal name. The governance
// proposal and UpgradeName must stay identical or the handler will not run.
func TestUpgradeName(t *testing.T) {
	require.Equal(t, "v0.2.18", UpgradeName)
}

// TestMigrateLegacyPruningLimits covers the backfill that moved here from the
// v0.2.16 handler once v0.2.16 had executed. Without it a chain carrying the
// original all-zero defaults keeps every pruner a no-op.
func TestMigrateLegacyPruningLimits(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.EpochParams.InferencePruningMax = 0
	params.EpochParams.PocPruningMax = -1
	require.NoError(t, k.SetParams(ctx, params))

	require.NoError(t, migrateLegacyPruningLimits(ctx, k))
	migrated, err := k.GetParams(ctx)
	require.NoError(t, err)
	require.Equal(t, inferencetypes.DefaultEpochParams().InferencePruningMax, migrated.EpochParams.InferencePruningMax)
	require.Equal(t, inferencetypes.DefaultEpochParams().PocPruningMax, migrated.EpochParams.PocPruningMax)
	require.NoError(t, migrated.Validate())

	// The migration is idempotent and preserves already-positive limits.
	migrated.EpochParams.InferencePruningMax = 123
	migrated.EpochParams.PocPruningMax = 456
	require.NoError(t, k.SetParams(ctx, migrated))
	require.NoError(t, migrateLegacyPruningLimits(ctx, k))
	again, err := k.GetParams(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(123), again.EpochParams.InferencePruningMax)
	require.Equal(t, int64(456), again.EpochParams.PocPruningMax)
}
