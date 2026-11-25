package v0_2_17

import (
	"testing"

	keepertest "github.com/productscience/inference/testutil/keeper"
	blstypes "github.com/productscience/inference/x/bls/types"
	"github.com/stretchr/testify/require"
)

func TestMigrateDKGQuorum_WritesOneHalf(t *testing.T) {
	k, ctx := keepertest.BlsKeeper(t)

	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.DkgQuorumNumerator = 0
	params.DkgQuorumDenominator = 0
	params.MaxDealerAttemptsPerEpoch = 0
	require.NoError(t, k.SetParams(ctx, params))

	require.NoError(t, MigrateDKGQuorum(ctx, k, ""))

	updated, err := k.GetParams(ctx)
	require.NoError(t, err)
	require.Equal(t, blstypes.DefaultDKGQuorumNumerator, updated.DkgQuorumNumerator)
	require.Equal(t, blstypes.DefaultDKGQuorumDenominator, updated.DkgQuorumDenominator)
	require.Equal(t, blstypes.DefaultMaxDealerAttemptsPerEpoch, updated.MaxDealerAttemptsPerEpoch)
	slots, err := updated.DKGQuorumSlots(100)
	require.NoError(t, err)
	require.Equal(t, uint64(51), slots)
}

func TestMigrateDKGQuorum_ReplacesAnyStoredFraction(t *testing.T) {
	k, ctx := keepertest.BlsKeeper(t)

	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.DkgQuorumNumerator = 2
	params.DkgQuorumDenominator = 3
	require.NoError(t, k.SetParams(ctx, params))

	require.NoError(t, MigrateDKGQuorum(ctx, k, `{"binaries":{"linux/amd64":"https://example.invalid/inferenced"}}`))

	updated, err := k.GetParams(ctx)
	require.NoError(t, err)
	require.Equal(t, uint32(1), updated.DkgQuorumNumerator)
	require.Equal(t, uint32(2), updated.DkgQuorumDenominator)
}

func TestMigrateDKGQuorum_UpgradeInfoSetsTwoThirds(t *testing.T) {
	k, ctx := keepertest.BlsKeeper(t)

	require.NoError(t, MigrateDKGQuorum(ctx, k, `{"dkg_quorum_numerator":2,"dkg_quorum_denominator":3}`))

	updated, err := k.GetParams(ctx)
	require.NoError(t, err)
	require.Equal(t, uint32(2), updated.DkgQuorumNumerator)
	require.Equal(t, uint32(3), updated.DkgQuorumDenominator)
	slots, err := updated.DKGQuorumSlots(100)
	require.NoError(t, err)
	require.Equal(t, uint64(67), slots)
}

func TestMigrateDKGQuorum_PartialOrInvalidUpgradeInfoRejected(t *testing.T) {
	k, ctx := keepertest.BlsKeeper(t)

	require.Error(t, MigrateDKGQuorum(ctx, k, `{"dkg_quorum_numerator":2}`))
	require.Error(t, MigrateDKGQuorum(ctx, k, `{"dkg_quorum_numerator":1,"dkg_quorum_denominator":1}`))
	require.Error(t, MigrateDKGQuorum(ctx, k, `{`))

	updated, err := k.GetParams(ctx)
	require.NoError(t, err)
	require.Equal(t, blstypes.DefaultDKGQuorumNumerator, updated.DkgQuorumNumerator)
	require.Equal(t, blstypes.DefaultDKGQuorumDenominator, updated.DkgQuorumDenominator)
}
