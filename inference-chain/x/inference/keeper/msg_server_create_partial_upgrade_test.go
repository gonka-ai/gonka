package keeper_test

import (
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/testutil"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

// Ensures permission checks for MsgCreatePartialUpgrade
func TestMsgServer_CreatePartialUpgrade_Permissions(t *testing.T) {
	k, ms, ctx := setupMsgServer(t)
	wctx := sdk.UnwrapSDKContext(ctx)

	// Non-authority should fail
	msg := &types.MsgCreatePartialUpgrade{Authority: testutil.Creator}
	err := keeper.CheckPermission(ms, wctx, msg, keeper.GovernancePermission)
	require.Error(t, err)

	// Authority should pass permission check
	msgOk := &types.MsgCreatePartialUpgrade{Authority: k.GetAuthority()}
	err = keeper.CheckPermission(ms, wctx, msgOk, keeper.GovernancePermission)
	require.NoError(t, err)
}
