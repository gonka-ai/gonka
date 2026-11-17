package app

import (
	"testing"
	"time"

	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authz "github.com/cosmos/cosmos-sdk/x/authz"
	"github.com/stretchr/testify/require"
	protov2 "google.golang.org/protobuf/proto"

	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/types"
)

type msgsTx struct{ msgs []sdk.Msg }

func (t msgsTx) GetMsgs() []sdk.Msg                    { return t.msgs }
func (t msgsTx) GetMsgsV2() ([]protov2.Message, error) { return nil, nil }

func postTestAddr(seed byte) sdk.AccAddress {
	b := make([]byte, 20)
	for i := range b {
		b[i] = seed
	}
	return sdk.AccAddress(b)
}

func newWarmKeyLogTestDecorator(t *testing.T) (WarmKeyLogDecorator, sdk.Context, func(sdk.AccAddress) []types.WarmKeyLogEntry) {
	t.Helper()
	k, ctx := keepertest.InferenceKeeper(t)
	registry := codectypes.NewInterfaceRegistry()
	authz.RegisterInterfaces(registry)
	cdc := codec.NewProtoCodec(registry)
	entries := func(granter sdk.AccAddress) []types.WarmKeyLogEntry {
		out, err := k.WarmKeyLogEntries(ctx, granter)
		require.NoError(t, err)
		return out
	}
	return NewWarmKeyLogDecorator(cdc, &k), ctx.WithExecMode(sdk.ExecModeFinalize).WithBlockHeight(42), entries
}

func passThrough(ctx sdk.Context, _ sdk.Tx, _, _ bool) (sdk.Context, error) { return ctx, nil }

// Test flow:
//  1. Run the decorator on a tx with a marker MsgGrant, a MsgGrant of another type and a marker MsgRevoke, in
//     FinalizeBlock with success.
//  2. Assert the log gains one entry for the marker grant and the revoke ends it; the other grant is ignored.
func TestWarmKeyLogDecoratorRecordsMarkerGrantsAndRevokes(t *testing.T) {
	d, ctx, entries := newWarmKeyLogTestDecorator(t)
	granter, grantee := postTestAddr(1), postTestAddr(2)
	expires := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)

	grant, err := authz.NewMsgGrant(granter, grantee, authz.NewGenericAuthorization(types.WarmKeyGrantMarkerTypeURL), &expires)
	require.NoError(t, err)
	other, err := authz.NewMsgGrant(granter, grantee, authz.NewGenericAuthorization("/cosmos.bank.v1beta1.MsgSend"), nil)
	require.NoError(t, err)
	_, err = d.PostHandle(ctx, msgsTx{msgs: []sdk.Msg{grant, other}}, false, true, passThrough)
	require.NoError(t, err)
	got := entries(granter)
	require.Len(t, got, 1)
	require.Equal(t, grantee.String(), got[0].Grantee)
	require.Equal(t, int64(42), got[0].GrantedAtHeight)
	require.Equal(t, expires.Unix(), got[0].ExpiresAtUnix)

	revoke := authz.NewMsgRevoke(granter, grantee, types.WarmKeyGrantMarkerTypeURL)
	otherRevoke := authz.NewMsgRevoke(granter, grantee, "/cosmos.bank.v1beta1.MsgSend")
	_, err = d.PostHandle(ctx.WithBlockHeight(43), msgsTx{msgs: []sdk.Msg{&otherRevoke, &revoke}}, false, true, passThrough)
	require.NoError(t, err)
	got = entries(granter)
	require.Len(t, got, 1)
	require.Equal(t, int64(43), got[0].RevokedAtHeight)
	require.Equal(t, types.WarmKeyLogEnd_WARM_KEY_LOG_END_REVOKED, got[0].End)
}

// Test flow:
//  1. Wrap a marker MsgGrant in a MsgExec and run the decorator.
//  2. Assert the nested grant is recorded for the inner granter.
func TestWarmKeyLogDecoratorWalksMsgExec(t *testing.T) {
	d, ctx, entries := newWarmKeyLogTestDecorator(t)
	granter, grantee, executor := postTestAddr(1), postTestAddr(2), postTestAddr(3)
	grant, err := authz.NewMsgGrant(granter, grantee, authz.NewGenericAuthorization(types.WarmKeyGrantMarkerTypeURL), nil)
	require.NoError(t, err)
	exec := authz.NewMsgExec(executor, []sdk.Msg{grant})

	_, err = d.PostHandle(ctx, msgsTx{msgs: []sdk.Msg{&exec}}, false, true, passThrough)
	require.NoError(t, err)
	require.Len(t, entries(granter), 1)
	require.Empty(t, entries(executor))
}

// Test flow:
//  1. Run the decorator on a marker grant in CheckTx mode, in simulation, and with success=false.
//  2. Assert nothing is recorded in any of the three.
func TestWarmKeyLogDecoratorRecordsOnlyFinalizedSuccess(t *testing.T) {
	d, ctx, entries := newWarmKeyLogTestDecorator(t)
	granter, grantee := postTestAddr(1), postTestAddr(2)
	grant, err := authz.NewMsgGrant(granter, grantee, authz.NewGenericAuthorization(types.WarmKeyGrantMarkerTypeURL), nil)
	require.NoError(t, err)
	tx := msgsTx{msgs: []sdk.Msg{grant}}

	_, err = d.PostHandle(ctx.WithExecMode(sdk.ExecModeCheck), tx, false, true, passThrough)
	require.NoError(t, err)
	_, err = d.PostHandle(ctx, tx, true, true, passThrough)
	require.NoError(t, err)
	_, err = d.PostHandle(ctx, tx, false, false, passThrough)
	require.NoError(t, err)
	require.Empty(t, entries(granter))
}
