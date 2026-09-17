package v0_2_16

import (
	"context"
	"errors"
	"testing"
	"time"

	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authz "github.com/cosmos/cosmos-sdk/x/authz"
	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference"
	inferencetypes "github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

type testGrant struct {
	granter sdk.AccAddress
	grantee sdk.AccAddress
	grant   authz.Grant
}

type savedGrant struct {
	granter    sdk.AccAddress
	grantee    sdk.AccAddress
	msgType    string
	expiration *time.Time
}

type mockAuthzKeeper struct {
	grants   []testGrant
	existing map[string]bool
	saved    []savedGrant
	saveErr  error
}

func grantKey(grantee, granter sdk.AccAddress, msgType string) string {
	return granter.String() + "->" + grantee.String() + ":" + msgType
}

func (m *mockAuthzKeeper) IterateGrants(_ context.Context, handler func(sdk.AccAddress, sdk.AccAddress, authz.Grant) bool) {
	for _, grant := range m.grants {
		if handler(grant.granter, grant.grantee, grant.grant) {
			return
		}
	}
}

func (m *mockAuthzKeeper) GetAuthorization(_ context.Context, grantee, granter sdk.AccAddress, msgType string) (authz.Authorization, *time.Time) {
	if m.existing[grantKey(grantee, granter, msgType)] {
		return authz.NewGenericAuthorization(msgType), nil
	}
	return nil, nil
}

func (m *mockAuthzKeeper) SaveGrant(_ context.Context, grantee, granter sdk.AccAddress, authorization authz.Authorization, expiration *time.Time) error {
	if m.saveErr != nil {
		return m.saveErr
	}
	m.saved = append(m.saved, savedGrant{granter: granter, grantee: grantee, msgType: authorization.MsgTypeURL(), expiration: expiration})
	return nil
}

func genericGrant(t *testing.T, granter, grantee sdk.AccAddress, msgType string, expiration *time.Time) testGrant {
	t.Helper()
	authorizationAny, err := codectypes.NewAnyWithValue(authz.NewGenericAuthorization(msgType))
	require.NoError(t, err)
	return testGrant{
		granter: granter,
		grantee: grantee,
		grant:   authz.Grant{Authorization: authorizationAny, Expiration: expiration},
	}
}

var (
	testGranter  = sdk.AccAddress([]byte("granter_____________"))
	testGrantee  = sdk.AccAddress([]byte("grantee_____________"))
	testGranter2 = sdk.AccAddress([]byte("granter2____________"))
	testGrantee2 = sdk.AccAddress([]byte("grantee2____________"))
	testNow      = time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	testExpiry   = time.Date(2027, 1, 2, 3, 4, 5, 0, time.UTC)
)

func trainingMsgTypes() (string, string) {
	return sdk.MsgTypeURL(&inferencetypes.MsgRefreshTrainingNodeOptIn{}), sdk.MsgTypeURL(&inferencetypes.MsgAutokickTrainshardNode{})
}

func TestGrantTrainingWarmKeyAuthz_CreatesBothGrantsPerPair(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	ctx = ctx.WithBlockTime(testNow)
	authzKeeper := &mockAuthzKeeper{
		grants: []testGrant{
			genericGrant(t, testGranter, testGrantee, inferencetypes.WarmKeyGrantMarkerTypeURL, &testExpiry),
			genericGrant(t, testGranter, testGrantee, inferencetypes.LegacyMsgStartInferenceTypeURL, &testExpiry),
			genericGrant(t, testGranter2, testGrantee2, inferencetypes.LegacyMsgStartInferenceTypeURL, nil),
			genericGrant(t, testGranter, testGrantee, "/inference.inference.MsgFinishInference", &testExpiry),
		},
	}

	require.NoError(t, grantTrainingWarmKeyAuthz(ctx, authzKeeper, k))

	refresh, autokick := trainingMsgTypes()
	require.Equal(t, []savedGrant{
		{granter: testGranter, grantee: testGrantee, msgType: refresh, expiration: &testExpiry},
		{granter: testGranter, grantee: testGrantee, msgType: autokick, expiration: &testExpiry},
		{granter: testGranter2, grantee: testGrantee2, msgType: refresh, expiration: nil},
		{granter: testGranter2, grantee: testGrantee2, msgType: autokick, expiration: nil},
	}, authzKeeper.saved)
}

func TestGrantTrainingWarmKeyAuthz_SkipsExistingAndExpired(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	ctx = ctx.WithBlockTime(testNow)
	refresh, autokick := trainingMsgTypes()
	expired := testNow.Add(-time.Hour)
	authzKeeper := &mockAuthzKeeper{
		grants: []testGrant{
			genericGrant(t, testGranter, testGrantee, inferencetypes.WarmKeyGrantMarkerTypeURL, &testExpiry),
			genericGrant(t, testGranter2, testGrantee2, inferencetypes.WarmKeyGrantMarkerTypeURL, &expired),
		},
		existing: map[string]bool{grantKey(testGrantee, testGranter, refresh): true},
	}

	require.NoError(t, grantTrainingWarmKeyAuthz(ctx, authzKeeper, k))

	require.Equal(t, []savedGrant{
		{granter: testGranter, grantee: testGrantee, msgType: autokick, expiration: &testExpiry},
	}, authzKeeper.saved)
}

func TestGrantTrainingWarmKeyAuthz_NoPairsIsNoop(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	authzKeeper := &mockAuthzKeeper{
		grants: []testGrant{
			genericGrant(t, testGranter, testGrantee, "/inference.inference.MsgFinishInference", &testExpiry),
		},
	}

	require.NoError(t, grantTrainingWarmKeyAuthz(ctx, authzKeeper, k))
	require.Empty(t, authzKeeper.saved)
}

func TestGrantTrainingWarmKeyAuthz_SaveErrorFailsUpgrade(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	ctx = ctx.WithBlockTime(testNow)
	authzKeeper := &mockAuthzKeeper{
		grants:  []testGrant{genericGrant(t, testGranter, testGrantee, inferencetypes.WarmKeyGrantMarkerTypeURL, &testExpiry)},
		saveErr: errors.New("boom"),
	}

	err := grantTrainingWarmKeyAuthz(ctx, authzKeeper, k)
	require.ErrorContains(t, err, "boom")
}

func TestTrainingWarmKeyMsgTypesAreOperationKeyPerms(t *testing.T) {
	perms := make(map[string]bool, len(inference.InferenceOperationKeyPerms))
	for _, msg := range inference.InferenceOperationKeyPerms {
		perms[sdk.MsgTypeURL(msg)] = true
	}
	for _, msgType := range trainingWarmKeyMsgTypeURLs {
		require.True(t, perms[msgType], msgType)
	}
}
