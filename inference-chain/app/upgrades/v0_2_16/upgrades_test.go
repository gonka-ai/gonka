package v0_2_16

import (
	"testing"

	"github.com/stretchr/testify/require"

	keepertest "github.com/productscience/inference/testutil/keeper"
	inferencetypes "github.com/productscience/inference/x/inference/types"
)

// TestUpgradeName pins the future on-chain proposal name. The governance
// proposal and UpgradeName must stay identical or the handler will not run.
func TestUpgradeName(t *testing.T) {
	require.Equal(t, "v0.2.16", UpgradeName)
}

func TestApplyDevshardApprovedVersionsSetsReportsValidated(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)

	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.DevshardEscrowParams = inferencetypes.DefaultDevshardEscrowParams()
	params.DevshardEscrowParams.ApprovedVersions = []*inferencetypes.DevshardApprovedVersion{
		{Name: "v6", Binary: "https://example.com/devshardd-v6.zip", Sha256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
	}
	require.NoError(t, k.SetParams(ctx, params))

	infoJSON := `{
		"binaries": {"linux/amd64": "https://example.com/inferenced.zip"},
		"approved_versions": [
			{
				"name": "v7",
				"binary": "https://example.com/devshardd-v7.zip",
				"sha256": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
				"reports_validated": true
			}
		]
	}`
	require.NoError(t, applyDevshardApprovedVersions(ctx, k, infoJSON))

	got, err := k.GetParams(ctx)
	require.NoError(t, err)
	require.Equal(t, []*inferencetypes.DevshardApprovedVersion{
		{Name: "v6", Binary: "https://example.com/devshardd-v6.zip", Sha256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		{Name: "v7", Binary: "https://example.com/devshardd-v7.zip", Sha256: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", ReportsValidated: true},
	}, got.DevshardEscrowParams.ApprovedVersions)
}

func TestApplyDevshardApprovedVersionsKeepsRecordedPolicyOnFlip(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)

	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.DevshardEscrowParams = inferencetypes.DefaultDevshardEscrowParams()
	params.DevshardEscrowParams.ApprovedVersions = []*inferencetypes.DevshardApprovedVersion{
		{Name: "v6", Binary: "https://example.com/devshardd-v6.zip", Sha256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
	}
	require.NoError(t, k.SetParams(ctx, params))

	infoJSON := `{"approved_versions": [{"name": "v6", "binary": "https://example.com/devshardd-v6b.zip", "sha256": "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", "reports_validated": true}]}`
	require.NoError(t, applyDevshardApprovedVersions(ctx, k, infoJSON), "an upgrade must not halt over a re-declared flag")

	got, err := k.GetParams(ctx)
	require.NoError(t, err)
	require.Equal(t, []*inferencetypes.DevshardApprovedVersion{
		{Name: "v6", Binary: "https://example.com/devshardd-v6b.zip", Sha256: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"},
	}, got.DevshardEscrowParams.ApprovedVersions, "binary updated, recorded policy kept")
	require.Equal(t, []*inferencetypes.DevshardVersionPolicy{{Name: "v6"}}, got.DevshardEscrowParams.VersionPolicies)
}

func TestApplyDevshardApprovedVersionsRecordsPreexistingPolicies(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)

	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.DevshardEscrowParams = inferencetypes.DefaultDevshardEscrowParams()
	params.DevshardEscrowParams.ApprovedVersions = []*inferencetypes.DevshardApprovedVersion{
		{Name: "v5", Binary: "https://example.com/devshardd-v5.zip", Sha256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		{Name: "v6", Binary: "https://example.com/devshardd-v6.zip", Sha256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
	}
	require.NoError(t, k.SetParams(ctx, params))

	infoJSON := `{"approved_versions": [{"name": "v7", "binary": "https://example.com/devshardd-v7.zip", "sha256": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "reports_validated": true}]}`
	require.NoError(t, applyDevshardApprovedVersions(ctx, k, infoJSON))

	got, err := k.GetParams(ctx)
	require.NoError(t, err)
	require.Equal(t, []*inferencetypes.DevshardVersionPolicy{
		{Name: "v5"}, {Name: "v6"}, {Name: "v7", ReportsValidated: true},
	}, got.DevshardEscrowParams.VersionPolicies, "pre-existing production approvals are recorded as legacy")
}
