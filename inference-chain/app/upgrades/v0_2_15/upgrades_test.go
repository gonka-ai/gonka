package v0_2_15

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authz "github.com/cosmos/cosmos-sdk/x/authz"
	keepertest "github.com/productscience/inference/testutil/keeper"
	inferencetypes "github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

type testGrant struct {
	granter sdk.AccAddress
	grantee sdk.AccAddress
	grant   authz.Grant
}

type mockAuthzKeeper struct {
	grants       []testGrant
	existing     authz.Authorization
	saved        authz.Authorization
	savedExpiry  *time.Time
	savedGranter sdk.AccAddress
	savedGrantee sdk.AccAddress
}

func (m *mockAuthzKeeper) IterateGrants(_ context.Context, handler func(sdk.AccAddress, sdk.AccAddress, authz.Grant) bool) {
	for _, grant := range m.grants {
		if handler(grant.granter, grant.grantee, grant.grant) {
			return
		}
	}
}

func (m *mockAuthzKeeper) GetAuthorization(_ context.Context, _, _ sdk.AccAddress, _ string) (authz.Authorization, *time.Time) {
	return m.existing, nil
}

func (m *mockAuthzKeeper) SaveGrant(_ context.Context, grantee, granter sdk.AccAddress, authorization authz.Authorization, expiration *time.Time) error {
	m.saved = authorization
	m.savedExpiry = expiration
	m.savedGranter = granter
	m.savedGrantee = grantee
	return nil
}

func legacyMarkerGrant(t *testing.T, granter, grantee sdk.AccAddress, expiration *time.Time) testGrant {
	t.Helper()
	authorization := authz.NewGenericAuthorization(inferencetypes.LegacyMsgStartInferenceTypeURL)
	authorizationAny, err := codectypes.NewAnyWithValue(authorization)
	require.NoError(t, err)
	return testGrant{
		granter: granter,
		grantee: grantee,
		grant:   authz.Grant{Authorization: authorizationAny, Expiration: expiration},
	}
}

// TestUpgradeName pins the future on-chain proposal name. The governance
// proposal and UpgradeName must stay identical or the handler will not run.
func TestUpgradeName(t *testing.T) {
	require.Equal(t, "v0.2.15", UpgradeName)
}

func TestMigrateWarmKeyGrantMarkerCreatesMissingGrant(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	granter := sdk.AccAddress([]byte("granter_____________"))
	grantee := sdk.AccAddress([]byte("grantee_____________"))
	expiration := time.Date(2027, 1, 2, 3, 4, 5, 0, time.UTC)
	authzKeeper := &mockAuthzKeeper{
		grants: []testGrant{legacyMarkerGrant(t, granter, grantee, &expiration)},
	}

	require.NoError(t, migrateWarmKeyGrantMarker(ctx, authzKeeper, k))
	require.Equal(t, authz.NewGenericAuthorization(inferencetypes.WarmKeyGrantMarkerTypeURL), authzKeeper.saved)
	require.Equal(t, &expiration, authzKeeper.savedExpiry)
	require.Equal(t, granter, authzKeeper.savedGranter)
	require.Equal(t, grantee, authzKeeper.savedGrantee)
}

func TestMigrateWarmKeyGrantMarkerSkipsExistingGrant(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	granter := sdk.AccAddress([]byte("granter_____________"))
	grantee := sdk.AccAddress([]byte("grantee_____________"))
	expiration := time.Date(2027, 1, 2, 3, 4, 5, 0, time.UTC)
	authzKeeper := &mockAuthzKeeper{
		grants:   []testGrant{legacyMarkerGrant(t, granter, grantee, &expiration)},
		existing: authz.NewGenericAuthorization(inferencetypes.WarmKeyGrantMarkerTypeURL),
	}

	require.NoError(t, migrateWarmKeyGrantMarker(ctx, authzKeeper, k))
	require.Nil(t, authzKeeper.saved)
}

func TestApplyDevshardApprovedVersionsAppendsAndUpdatesMirrors(t *testing.T) {
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
				"sha256": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
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
			Sha256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		},
		{
			Name:   "v2",
			Binary: "https://example.com/devshardd-v2.zip",
			Sha256: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
		},
	}, got.DevshardEscrowParams.ApprovedVersions)
}

func TestApplyDevshardApprovedVersionsRejectsSameNameNewSHA(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)

	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.DevshardEscrowParams = inferencetypes.DefaultDevshardEscrowParams()
	params.DevshardEscrowParams.ApprovedVersions = []*inferencetypes.DevshardApprovedVersion{{
		Name:   "v1",
		Binary: "https://example.com/devshardd-v1.zip",
		Sha256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}}
	require.NoError(t, k.SetParams(ctx, params))

	err = applyDevshardApprovedVersions(ctx, k, `{
		"approved_versions": [{
			"name": "v1",
			"binary": "https://example.com/devshardd-v1-new.zip",
			"sha256": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		}]
	}`)
	require.ErrorContains(t, err, `approved devshard version "v1" sha256 is immutable`)
}

func TestApplyDevshardApprovedVersionsRejectsNullVersion(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)

	err := applyDevshardApprovedVersions(ctx, k, `{"approved_versions":[null]}`)

	require.EqualError(t, err, "approved_versions[0] cannot be null")
}

func TestApplyDevshardApprovedVersionsUsesConsensusValidation(t *testing.T) {
	tests := []struct {
		name    string
		version *inferencetypes.DevshardApprovedVersion
		want    string
	}{
		{
			name: "invalid name",
			version: &inferencetypes.DevshardApprovedVersion{
				Name:   "v5;candidate",
				Binary: "https://example.com/devshardd-v5.zip",
				Sha256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			},
			want: "invalid name",
		},
		{
			name: "invalid sha",
			version: &inferencetypes.DevshardApprovedVersion{
				Name:   "v5",
				Binary: "https://example.com/devshardd-v5.zip",
				Sha256: "not-a-sha",
			},
			want: "sha256 must be 64 hex characters",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
			before, err := k.GetParams(ctx)
			require.NoError(t, err)
			encoded, err := json.Marshal(UpgradeInfo{
				ApprovedVersions: []*inferencetypes.DevshardApprovedVersion{tt.version},
			})
			require.NoError(t, err)

			err = applyDevshardApprovedVersions(ctx, k, string(encoded))
			require.ErrorContains(t, err, tt.want)
			after, getErr := k.GetParams(ctx)
			require.NoError(t, getErr)
			require.Equal(t, before, after)
		})
	}
}

func TestApplyDevshardApprovedVersionsRejectsCatalogOverCapacity(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	info := UpgradeInfo{}
	for i := 0; i <= inferencetypes.MaxDevshardApprovedVersions; i++ {
		info.ApprovedVersions = append(info.ApprovedVersions,
			&inferencetypes.DevshardApprovedVersion{
				Name:   fmt.Sprintf("v%d", i),
				Binary: "https://example.com/devshardd.zip",
				Sha256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			})
	}
	encoded, err := json.Marshal(info)
	require.NoError(t, err)

	err = applyDevshardApprovedVersions(ctx, k, string(encoded))
	require.ErrorContains(t, err, "maximum is 32")
}

func TestApplyDevshardApprovedVersionsDoesNotGrandfatherInvalidCatalog(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.DevshardEscrowParams = inferencetypes.DefaultDevshardEscrowParams()
	params.DevshardEscrowParams.ApprovedVersions = []*inferencetypes.DevshardApprovedVersion{
		{
			Name:   "legacy;invalid",
			Binary: "https://example.com/legacy.zip",
			Sha256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		},
	}
	// SetParams is intentionally a raw store primitive. This simulates state
	// written by software from before the catalog grammar became consensus.
	require.NoError(t, k.SetParams(ctx, params))

	err = applyDevshardApprovedVersions(ctx, k, `{
		"approved_versions":[{
			"name":"v5",
			"binary":"https://example.com/v5.zip",
			"sha256":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		}]
	}`)
	require.ErrorContains(t, err, "legacy;invalid")
}

func TestApplyDevshardApprovedVersionsValidatesLiveCatalogWhenInfoIsEmpty(t *testing.T) {
	t.Run("empty live catalog", func(t *testing.T) {
		k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
		err := applyDevshardApprovedVersions(ctx, k, "")
		require.ErrorContains(t, err, "current devshard catalog is empty")
	})

	t.Run("invalid live catalog", func(t *testing.T) {
		k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
		params, err := k.GetParams(ctx)
		require.NoError(t, err)
		params.DevshardEscrowParams = inferencetypes.DefaultDevshardEscrowParams()
		params.DevshardEscrowParams.ApprovedVersions = []*inferencetypes.DevshardApprovedVersion{{
			Name:   "invalid;legacy",
			Binary: "https://example.com/legacy.zip",
			Sha256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		}}
		require.NoError(t, k.SetParams(ctx, params))

		err = applyDevshardApprovedVersions(ctx, k, `{}`)
		require.ErrorContains(t, err, "invalid;legacy")
	})

	t.Run("valid live catalog", func(t *testing.T) {
		k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
		params, err := k.GetParams(ctx)
		require.NoError(t, err)
		params.DevshardEscrowParams = inferencetypes.DefaultDevshardEscrowParams()
		params.DevshardEscrowParams.ApprovedVersions = []*inferencetypes.DevshardApprovedVersion{{
			Name:   "v4",
			Binary: "https://example.com/v4.zip",
			Sha256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		}}
		require.NoError(t, k.SetParams(ctx, params))

		require.NoError(t, applyDevshardApprovedVersions(ctx, k, ""))
	})
}
