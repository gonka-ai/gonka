package keeper_test

import (
	"testing"

	"cosmossdk.io/math"
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

func TestMsgServer_RequestBridgeMint_Behavior(t *testing.T) {
	k, ms, ctx, mocks := setupKeeperWithMocks(t)
	wctx := sdk.UnwrapSDKContext(ctx)

	// Valid setup
	creator := testutil.Creator
	signer, _ := sdk.AccAddressFromBech32(creator)
	mocks.AccountKeeper.EXPECT().HasAccount(wctx, signer).Return(true).AnyTimes()
	mocks.BankViewKeeper.EXPECT().SpendableCoin(wctx, signer, types.BaseCoin).Return(sdk.NewCoin(types.BaseCoin, math.NewInt(1000))).Times(1)

	// Register one valid bridge address so chain validation passes first.
	k.SetBridgeContractAddress(wctx, types.BridgeContractAddress{
		ChainId: "ethereum",
		Address: "0x1111111111111111111111111111111111111111",
	})

	msg := &types.MsgRequestBridgeMint{
		Creator:                  creator,
		Amount:                   "100",
		DestinationAddress:       "0x3333333333333333333333333333333333333333",
		ChainId:                  "ethereum",
		DestinationBridgeAddress: "0x2222222222222222222222222222222222222222",
	}

	// Invalid bridge address should be rejected.
	// Only 0x111...111 is registered for ethereum.
	_, err := ms.RequestBridgeMint(ctx, msg)
	require.Error(t, err)
}

func TestMsgServer_RequestBridgeMint_RejectsBridgeAsDestination(t *testing.T) {
	k, ms, ctx, mocks := setupKeeperWithMocks(t)
	wctx := sdk.UnwrapSDKContext(ctx)

	signer, _ := sdk.AccAddressFromBech32(testutil.Creator)
	mocks.AccountKeeper.EXPECT().HasAccount(wctx, signer).Return(true).AnyTimes()
	mocks.BankViewKeeper.EXPECT().SpendableCoin(wctx, signer, types.BaseCoin).Return(sdk.NewCoin(types.BaseCoin, math.NewInt(1000))).AnyTimes()

	bridge := "0x972a7a92d92796a98801a8818bcf91f1648f2f68"
	k.SetBridgeContractAddress(wctx, types.BridgeContractAddress{ChainId: "ethereum", Address: bridge})

	_, err := ms.RequestBridgeMint(ctx, &types.MsgRequestBridgeMint{
		Creator:                  testutil.Creator,
		Amount:                   "100",
		DestinationAddress:       "0x972a7A92D92796a98801A8818bcF91f1648f2F68",
		ChainId:                  "ethereum",
		DestinationBridgeAddress: bridge,
	})
	require.ErrorContains(t, err, "is a bridge contract")
}
