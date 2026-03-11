package keeper_test

import (
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/testutil"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func TestMsgServer_RequestBridgeMint_Permissions(t *testing.T) {
	_, ms, ctx, mocks := setupKeeperWithMocks(t)
	wctx := sdk.UnwrapSDKContext(ctx)

	// Non-existent account should fail
	signer, _ := sdk.AccAddressFromBech32(testutil.Creator)
	mocks.AccountKeeper.EXPECT().HasAccount(wctx, signer).Return(false)
	msg := &types.MsgRequestBridgeMint{Creator: testutil.Creator}
	err := keeper.CheckPermission(ms, wctx, msg, keeper.AccountPermission)
	require.Error(t, err)

	// Existing account should pass
	mocks.AccountKeeper.EXPECT().HasAccount(wctx, signer).Return(true)
	err = keeper.CheckPermission(ms, wctx, msg, keeper.AccountPermission)
	require.NoError(t, err)
}
