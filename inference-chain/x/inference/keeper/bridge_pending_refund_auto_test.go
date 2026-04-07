package keeper_test

import (
	"bytes"
	"encoding/hex"
	"testing"

	"cosmossdk.io/collections"
	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/testutil"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestProcessAutoRefundForFailedBridgeOperation_Mint(t *testing.T) {
	k, _, ctx, mocks := setupKeeperWithMocks(t)
	requestID := bytes.Repeat([]byte{0x44}, 32)
	requestKey := hex.EncodeToString(requestID)

	require.NoError(t, k.BridgeMintRefundsMap.Set(ctx, requestKey, types.MsgRequestBridgeMint{
		Creator:            testutil.Creator,
		Amount:             "1000",
		DestinationAddress: "0xabc",
		ChainId:            "ethereum",
	}))

	creatorAddr, err := sdk.AccAddressFromBech32(testutil.Creator)
	require.NoError(t, err)
	refundCoins := sdk.NewCoins(sdk.NewCoin(types.BaseCoin, math.NewInt(1000)))
	mocks.BankKeeper.EXPECT().
		SendCoinsFromModuleToAccount(gomock.Any(), types.BridgeEscrowAccName, creatorAddr, refundCoins, "bridge_release").
		Return(nil).
		Times(1)

	require.NoError(t, k.ProcessAutoRefundForFailedBridgeOperation(ctx, requestID, "deadline expired"))

	_, err = k.BridgeMintRefundsMap.Get(ctx, requestKey)
	require.ErrorIs(t, err, collections.ErrNotFound)

	found := false
	for _, event := range ctx.EventManager().Events() {
		if event.Type == "bridge_operation_auto_refunded" {
			found = true
			break
		}
	}
	require.True(t, found)
}

func TestProcessAutoRefundForFailedBridgeOperation_Withdrawal(t *testing.T) {
	k, _, ctx, _ := setupKeeperWithMocks(t)
	requestID := bytes.Repeat([]byte{0x55}, 32)
	requestKey := hex.EncodeToString(requestID)

	require.NoError(t, k.BridgeWithdrawalRefundsMap.Set(ctx, requestKey, types.MsgRequestBridgeWithdrawal{
		Creator:            testutil.Creator,
		UserAddress:        testutil.Requester,
		Amount:             "1000",
		DestinationAddress: "0xabc",
	}))

	var mintCalls int
	k.SetMintTokensFnForTesting(func(_ sdk.Context, contractAddr, recipient, amount string) error {
		mintCalls++
		require.Equal(t, testutil.Creator, contractAddr)
		require.Equal(t, testutil.Requester, recipient)
		require.Equal(t, "1000", amount)
		return nil
	})

	require.NoError(t, k.ProcessAutoRefundForFailedBridgeOperation(ctx, requestID, "signature aggregation failed"))
	require.Equal(t, 1, mintCalls)

	_, err := k.BridgeWithdrawalRefundsMap.Get(ctx, requestKey)
	require.ErrorIs(t, err, collections.ErrNotFound)

	found := false
	for _, event := range ctx.EventManager().Events() {
		if event.Type == "bridge_operation_auto_refunded" {
			found = true
			break
		}
	}
	require.True(t, found)
}

func TestProcessAutoRefundForFailedBridgeOperation_NoPendingContext(t *testing.T) {
	k, _, ctx, _ := setupKeeperWithMocks(t)
	requestID := bytes.Repeat([]byte{0x66}, 32)

	require.NoError(t, k.ProcessAutoRefundForFailedBridgeOperation(ctx, requestID, "deadline expired"))
}
