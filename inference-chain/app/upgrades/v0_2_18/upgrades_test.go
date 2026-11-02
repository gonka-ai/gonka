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

func TestStampUnpoliciedApprovedVersionsDerived(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	require.NoError(t, k.SetApprovedVersion(ctx, inferencetypes.DevshardApprovedVersion{
		Name:   "v4.1",
		Binary: "https://example.com/v4.1.zip",
		Sha256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	}))
	require.NoError(t, k.SetApprovedVersion(ctx, inferencetypes.DevshardApprovedVersion{
		Name:   "v4.2",
		Binary: "https://example.com/v4.2.zip",
		Sha256: "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789",
	}))
	sampled := inferencetypes.DevshardApprovedVersion{
		Name:      "v-sampled",
		Binary:    "https://example.com/sampled.zip",
		Sha256:    "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff",
		PassCount: inferencetypes.DevshardPassCount_DEVSHARD_PASS_COUNT_SAMPLED,
	}
	require.NoError(t, k.SetApprovedVersion(ctx, sampled))
	require.NoError(t, k.SetVersionPolicy(ctx, inferencetypes.DevshardVersionPolicy{
		Name:      sampled.Name,
		PassCount: inferencetypes.DevshardPassCount_DEVSHARD_PASS_COUNT_SAMPLED,
	}))

	require.NoError(t, stampUnpoliciedApprovedVersionsDerived(ctx, k))

	for _, name := range []string{"v4.1", "v4.2"} {
		got, found := k.GetApprovedVersion(ctx, name)
		require.True(t, found)
		require.Equal(t, inferencetypes.DevshardPassCount_DEVSHARD_PASS_COUNT_DERIVED, got.PassCount)
		pol, found, err := k.GetVersionPolicy(ctx, name)
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, inferencetypes.DevshardPassCount_DEVSHARD_PASS_COUNT_DERIVED, pol.PassCount)
	}

	got, found := k.GetApprovedVersion(ctx, sampled.Name)
	require.True(t, found)
	require.Equal(t, inferencetypes.DevshardPassCount_DEVSHARD_PASS_COUNT_SAMPLED, got.PassCount)
	pol, found, err := k.GetVersionPolicy(ctx, sampled.Name)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, inferencetypes.DevshardPassCount_DEVSHARD_PASS_COUNT_SAMPLED, pol.PassCount)

	require.NoError(t, stampUnpoliciedApprovedVersionsDerived(ctx, k))
	pol, found, err = k.GetVersionPolicy(ctx, "v4.1")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, inferencetypes.DevshardPassCount_DEVSHARD_PASS_COUNT_DERIVED, pol.PassCount)
}
