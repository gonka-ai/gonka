package keeper_test

import (
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func TestGetOrCreateWrappedTokenContract_RejectsBridgeAddress(t *testing.T) {
	k, ms, ctx := setupMsgServer(t)
	wctx := sdk.UnwrapSDKContext(ctx)

	chainId := "ethereum"
	bridgeAddress := "0xbridge1234567890abcdef1234567890abcdef12"

	// Register it as a bridge address via msgServer
	msg := &types.MsgRegisterBridgeAddresses{
		Authority: k.GetAuthority(),
		ChainName: chainId,
		Addresses: []string{bridgeAddress},
	}
	_, err := ms.RegisterBridgeAddresses(ctx, msg)
	require.NoError(t, err)

	// Attempt to create wrapped token
	_, err = k.GetOrCreateWrappedTokenContract(wctx, chainId, bridgeAddress)
	require.Error(t, err)
	require.Contains(t, err.Error(), "is a registered bridge contract address and cannot be used as a wrapped token")
}
