package v0_2_15

import (
	"testing"

	keepertest "github.com/productscience/inference/testutil/keeper"
	inferencetypes "github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

// TestUpgradeName pins the future on-chain proposal name. The governance
// proposal and UpgradeName must stay identical or the handler will not run.
func TestUpgradeName(t *testing.T) {
	require.Equal(t, "v0.2.15", UpgradeName)
}

func TestApplyDevshardApprovedVersionsAppendsAndReplaces(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)

	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.DevshardEscrowParams = inferencetypes.DefaultDevshardEscrowParams()
	params.DevshardEscrowParams.ApprovedVersions = []*inferencetypes.DevshardApprovedVersion{
		{
			Name:   "v1",
			Binary: "https://example.com/devshardd-v1-old.zip",
			Sha256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		},
	}
	require.NoError(t, k.SetParams(ctx, params))

	infoJSON := `{
		"binaries": {"linux/amd64": "https://example.com/inferenced.zip"},
		"api_binaries": {"linux/amd64": "https://example.com/decentralized-api.zip"},
		"approved_versions": [
			{
				"name": "v1",
				"binary": "https://example.com/devshardd-v1-new.zip",
				"sha256": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
			},
			{
				"name": "v2",
				"binary": "https://example.com/devshardd-v2.zip",
				"sha256": "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
			}
		]
	}`

	require.NoError(t, applyDevshardApprovedVersions(ctx, k, infoJSON))

	got, err := k.GetParams(ctx)
	require.NoError(t, err)
	require.Equal(t, []*inferencetypes.DevshardApprovedVersion{
		{
			Name:   "v1",
			Binary: "https://example.com/devshardd-v1-new.zip",
			Sha256: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		},
		{
			Name:   "v2",
			Binary: "https://example.com/devshardd-v2.zip",
			Sha256: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
		},
	}, got.DevshardEscrowParams.ApprovedVersions)
}

func TestApplyDevshardApprovedVersionsRejectsNullVersion(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)

	err := applyDevshardApprovedVersions(ctx, k, `{"approved_versions":[null]}`)

	require.EqualError(t, err, "approved_versions[0] cannot be null")
}
