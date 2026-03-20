package keeper_test

import (
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/testutil"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func TestMsgServer_MigrateAllWrappedTokens_Permissions(t *testing.T) {
	k, ms, ctx := setupMsgServer(t)
	wctx := sdk.UnwrapSDKContext(ctx)

	// Non-gov should fail
	msg := &types.MsgMigrateAllWrappedTokens{Authority: testutil.Creator}
	err := keeper.CheckPermission(ms, wctx, msg, keeper.GovernancePermission)
	require.Error(t, err)

	// Gov should pass
	msgOk := &types.MsgMigrateAllWrappedTokens{Authority: k.GetAuthority()}
	err = keeper.CheckPermission(ms, wctx, msgOk, keeper.GovernancePermission)
	require.NoError(t, err)
}
