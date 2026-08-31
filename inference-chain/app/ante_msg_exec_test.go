package app

import (
	"context"
	"testing"
	"time"

	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authz "github.com/cosmos/cosmos-sdk/x/authz"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	"github.com/productscience/inference/testutil"
	keepertest "github.com/productscience/inference/testutil/keeper"
	blstypes "github.com/productscience/inference/x/bls/types"
	inferencemodulekeeper "github.com/productscience/inference/x/inference/keeper"
	inferencetypes "github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

type mockAuthzGrantKeeper struct {
	grants map[string]authz.Authorization
}

func grantLookupKey(grantee, granter sdk.AccAddress, msgType string) string {
	return grantee.String() + "/" + granter.String() + "/" + msgType
}

func (m *mockAuthzGrantKeeper) GetAuthorization(_ context.Context, grantee, granter sdk.AccAddress, msgType string) (authz.Authorization, *time.Time) {
	if m == nil || m.grants == nil {
		return nil, nil
	}
	auth, ok := m.grants[grantLookupKey(grantee, granter, msgType)]
	if !ok {
		return nil, nil
	}
	return auth, nil
}

func (m *mockAuthzGrantKeeper) save(grantee, granter sdk.AccAddress, authorization authz.Authorization) {
	if m.grants == nil {
		m.grants = map[string]authz.Authorization{}
	}
	m.grants[grantLookupKey(grantee, granter, authorization.MsgTypeURL())] = authorization
}

func setupMsgExecAuthzAnte(t *testing.T) (inferencemodulekeeper.Keeper, sdk.Context, *mockAuthzGrantKeeper, MsgExecAuthorizationDecorator) {
	t.Helper()

	k, goCtx, _ := keepertest.InferenceKeeperReturningMocks(t)
	ctx := sdk.UnwrapSDKContext(goCtx).WithIsCheckTx(true).WithValue(networkDutyFeeBypassKey{}, true)

	ak := &mockAuthzGrantKeeper{}
	return k, ctx, ak, NewMsgExecAuthorizationDecorator(testMsgCodec(t), ak)
}

func seedMsg(creator string) *inferencetypes.MsgSubmitSeed {
	return &inferencetypes.MsgSubmitSeed{
		Creator:    creator,
		EpochIndex: 1,
		Signature:  "00000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000",
	}
}

func wrapExec(t *testing.T, grantee string, inner sdk.Msg) *authz.MsgExec {
	t.Helper()
	anyMsg, err := codectypes.NewAnyWithValue(inner)
	require.NoError(t, err)
	return &authz.MsgExec{
		Grantee: grantee,
		Msgs:    []*codectypes.Any{anyMsg},
	}
}

func runMsgExecAuthzAnte(t *testing.T, decorator MsgExecAuthorizationDecorator, ctx sdk.Context, msgs ...sdk.Msg) (bool, error) {
	t.Helper()
	nextCalled := false
	next := func(ctx sdk.Context, tx sdk.Tx, simulate bool) (sdk.Context, error) {
		nextCalled = true
		return ctx, nil
	}
	_, err := decorator.AnteHandle(ctx, testFeeTx{msgs: msgs}, false, next)
	return nextCalled, err
}

func TestMsgExecAuthorizationDecorator_ValidGenericGrantPasses(t *testing.T) {
	_, ctx, ak, decorator := setupMsgExecAuthzAnte(t)

	granter := sdk.MustAccAddressFromBech32(testutil.Creator)
	grantee := sdk.MustAccAddressFromBech32(testutil.Executor)
	inner := seedMsg(testutil.Creator)
	ak.save(grantee, granter, authz.NewGenericAuthorization(sdk.MsgTypeURL(inner)))

	nextCalled, err := runMsgExecAuthzAnte(t, decorator, ctx, wrapExec(t, testutil.Executor, inner))
	require.NoError(t, err)
	require.True(t, nextCalled)
}

func TestMsgExecAuthorizationDecorator_NoGrantFails(t *testing.T) {
	_, ctx, _, decorator := setupMsgExecAuthzAnte(t)

	nextCalled, err := runMsgExecAuthzAnte(t, decorator, ctx, wrapExec(t, testutil.Executor, seedMsg(testutil.Creator)))
	require.ErrorIs(t, err, authz.ErrNoAuthorizationFound)
	require.False(t, nextCalled)
}

func TestMsgExecAuthorizationDecorator_ExpiredGrantFails(t *testing.T) {
	_, ctx, _, decorator := setupMsgExecAuthzAnte(t)

	// GetAuthorization returns nil for expired grants; the mock models that
	// by simply not storing a grant for this pair/type.
	nextCalled, err := runMsgExecAuthzAnte(t, decorator, ctx, wrapExec(t, testutil.Executor, seedMsg(testutil.Creator)))
	require.ErrorIs(t, err, authz.ErrNoAuthorizationFound)
	require.False(t, nextCalled)
}

func TestMsgExecAuthorizationDecorator_WrongMessageTypeFails(t *testing.T) {
	_, ctx, ak, decorator := setupMsgExecAuthzAnte(t)

	granter := sdk.MustAccAddressFromBech32(testutil.Creator)
	grantee := sdk.MustAccAddressFromBech32(testutil.Executor)
	inner := seedMsg(testutil.Creator)
	ak.save(grantee, granter, authz.NewGenericAuthorization(sdk.MsgTypeURL(&inferencetypes.MsgSubmitHardwareDiff{})))

	nextCalled, err := runMsgExecAuthzAnte(t, decorator, ctx, wrapExec(t, testutil.Executor, inner))
	require.ErrorIs(t, err, authz.ErrNoAuthorizationFound)
	require.False(t, nextCalled)
}

func TestMsgExecAuthorizationDecorator_GranterEqualsGrantee_NoGrantRequired(t *testing.T) {
	_, ctx, _, decorator := setupMsgExecAuthzAnte(t)

	nextCalled, err := runMsgExecAuthzAnte(t, decorator, ctx, wrapExec(t, testutil.Creator, seedMsg(testutil.Creator)))
	require.NoError(t, err)
	require.True(t, nextCalled)
}

func TestMsgExecAuthorizationDecorator_MalformedMsgExecFailsClosed(t *testing.T) {
	_, ctx, _, decorator := setupMsgExecAuthzAnte(t)

	execMsg := &authz.MsgExec{
		Grantee: testutil.Executor,
		Msgs: []*codectypes.Any{{
			TypeUrl: sdk.MsgTypeURL(&inferencetypes.MsgSubmitSeed{}),
			Value:   []byte{0xff, 0x00, 0x01},
		}},
	}

	nextCalled, err := runMsgExecAuthzAnte(t, decorator, ctx, execMsg)
	require.Error(t, err)
	require.False(t, nextCalled)
}

func TestMsgExecAuthorizationDecorator_NestedMsgExecFailsClosed(t *testing.T) {
	_, ctx, ak, decorator := setupMsgExecAuthzAnte(t)

	granter := sdk.MustAccAddressFromBech32(testutil.Creator)
	grantee := sdk.MustAccAddressFromBech32(testutil.Executor)
	inner := seedMsg(testutil.Creator)
	ak.save(grantee, granter, authz.NewGenericAuthorization(sdk.MsgTypeURL(inner)))

	innerExec := authz.NewMsgExec(grantee, []sdk.Msg{inner})
	outer := authz.NewMsgExec(grantee, []sdk.Msg{&innerExec})

	nextCalled, err := runMsgExecAuthzAnte(t, decorator, ctx, &outer)
	require.ErrorIs(t, err, errNestedMsgExec)
	require.False(t, nextCalled)
}

func TestMsgExecAuthorizationDecorator_SkipsWhenNotCheckTx(t *testing.T) {
	_, ctx, _, decorator := setupMsgExecAuthzAnte(t)
	ctx = ctx.WithIsCheckTx(false)

	nextCalled, err := runMsgExecAuthzAnte(t, decorator, ctx, wrapExec(t, testutil.Executor, seedMsg(testutil.Creator)))
	require.NoError(t, err)
	require.True(t, nextCalled)
}

func TestMsgExecAuthorizationDecorator_SkipsWhenNotFeeBypassed(t *testing.T) {
	_, ctx, _, decorator := setupMsgExecAuthzAnte(t)
	ctx = ctx.WithValue(networkDutyFeeBypassKey{}, false)

	nextCalled, err := runMsgExecAuthzAnte(t, decorator, ctx, wrapExec(t, testutil.Executor, seedMsg(testutil.Creator)))
	require.NoError(t, err)
	require.True(t, nextCalled)
}

func TestMsgExecAuthorizationDecorator_InnerDealerPartValidateBasicFails(t *testing.T) {
	_, ctx, ak, decorator := setupMsgExecAuthzAnte(t)

	granter := sdk.MustAccAddressFromBech32(testutil.Creator)
	grantee := sdk.MustAccAddressFromBech32(testutil.Executor)
	commitment := make([]byte, 96)
	commitment[0] = 0x01
	short := make([]byte, 98)
	short[0] = 0x04
	inner := &blstypes.MsgSubmitDealerPart{
		Creator:     testutil.Creator,
		EpochId:     1,
		Commitments: [][]byte{commitment},
		EncryptedSharesForParticipants: []blstypes.EncryptedSharesForParticipant{{
			EncryptedShares: [][]byte{short},
		}},
	}
	ak.save(grantee, granter, authz.NewGenericAuthorization(sdk.MsgTypeURL(inner)))

	nextCalled, err := runMsgExecAuthzAnte(t, decorator, ctx, wrapExec(t, testutil.Executor, inner))
	require.Error(t, err)
	require.Contains(t, err.Error(), "below ECIES minimum")
	require.False(t, nextCalled)
}

func TestMsgExecAuthorizationDecorator_DirectMessagePassesThrough(t *testing.T) {
	_, ctx, _, decorator := setupMsgExecAuthzAnte(t)

	nextCalled, err := runMsgExecAuthzAnte(t, decorator, ctx, seedMsg(testutil.Creator))
	require.NoError(t, err)
	require.True(t, nextCalled)
}

func TestMsgExecAuthorizationDecorator_NonGenericAuthorizationFails(t *testing.T) {
	_, ctx, ak, decorator := setupMsgExecAuthzAnte(t)

	granter := sdk.MustAccAddressFromBech32(testutil.Creator)
	grantee := sdk.MustAccAddressFromBech32(testutil.Executor)
	inner := seedMsg(testutil.Creator)
	if ak.grants == nil {
		ak.grants = map[string]authz.Authorization{}
	}
	ak.grants[grantLookupKey(grantee, granter, sdk.MsgTypeURL(inner))] = &banktypes.SendAuthorization{}

	nextCalled, err := runMsgExecAuthzAnte(t, decorator, ctx, wrapExec(t, testutil.Executor, inner))
	require.ErrorIs(t, err, authz.ErrNoAuthorizationFound)
	require.False(t, nextCalled)
}
