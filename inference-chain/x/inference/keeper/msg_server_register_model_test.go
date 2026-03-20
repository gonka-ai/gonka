package keeper_test

import (
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/testutil"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func TestMsgServer_RegisterModel_Permissions(t *testing.T) {
	k, ms, ctx := setupMsgServer(t)
	wctx := sdk.UnwrapSDKContext(ctx)

	// Non-authority should fail
	msg := &types.MsgRegisterModel{Authority: testutil.Creator}
	err := keeper.CheckPermission(ms, wctx, msg, keeper.GovernancePermission)
	require.Error(t, err)

	// Authority should pass
	ok := &types.MsgRegisterModel{Authority: k.GetAuthority()}
	err = keeper.CheckPermission(ms, wctx, ok, keeper.GovernancePermission)
	require.NoError(t, err)
}
