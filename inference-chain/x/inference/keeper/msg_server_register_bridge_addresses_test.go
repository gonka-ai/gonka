package keeper_test

import (
	"context"
	"testing"
	
	wasmtypes "github.com/CosmWasm/wasmd/x/wasm/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/testutil"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func TestMsgServer_RegisterBridgeAddresses_Permissions(t *testing.T) {
	k, ms, ctx := setupMsgServer(t)
	wctx := sdk.UnwrapSDKContext(ctx)

	// Non-authority should fail
	msg := &types.MsgRegisterBridgeAddresses{Authority: testutil.Creator}
	err := keeper.CheckPermission(ms, wctx, msg, keeper.GovernancePermission)
	require.Error(t, err)

	// Authority should pass
	ok := &types.MsgRegisterBridgeAddresses{Authority: k.GetAuthority()}
	err = keeper.CheckPermission(ms, wctx, ok, keeper.GovernancePermission)
	require.NoError(t, err)
}

func TestMsgServer_RegisterBridgeAddresses_CleanupExistingWrappedToken(t *testing.T) {
	k, ms, ctx := setupMsgServer(t)
	wctx := sdk.UnwrapSDKContext(ctx)

	chainId := "ethereum"
	bridgeAddress := "0x1234567890abcdef1234567890abcdef12345678"
	wrappedContractAddr := sdk.AccAddress([]byte("contract_addr______")).String()
	authority := k.GetAuthority()

	// 1. Setup existing CW20 mapping, metadata, and AMM approval
	err := k.SetWrappedTokenContract(wctx, types.BridgeWrappedTokenContract{
		ChainId:                chainId,
		ContractAddress:        bridgeAddress,
		WrappedContractAddress: wrappedContractAddr,
	})
	require.NoError(t, err)

	err = k.SetTokenMetadata(wctx, chainId, bridgeAddress, keeper.TokenMetadata{
		Name:     "Test Token",
		Symbol:   "TEST",
		Decimals: 18,
	})
	require.NoError(t, err)

	err = k.SetBridgeTradeApprovedToken(wctx, types.BridgeTokenReference{
		ChainId:         chainId,
		ContractAddress: bridgeAddress,
	})
	require.NoError(t, err)

	// Verify they are set
	_, found := k.GetWrappedTokenContract(wctx, chainId, bridgeAddress)
	require.True(t, found)
	_, found = k.GetWrappedTokenContractByWrappedAddress(wctx, wrappedContractAddr)
	require.True(t, found)
	_, found = k.GetTokenMetadata(wctx, chainId, bridgeAddress)
	require.True(t, found)
	has := k.HasBridgeTradeApprovedToken(wctx, chainId, bridgeAddress)
	require.True(t, has)

	// 2. Register the bridge address (triggering cleanup)
	msg := &types.MsgRegisterBridgeAddresses{
		Authority: authority,
		ChainName: chainId,
		Addresses: []string{bridgeAddress},
	}
	_, err = ms.RegisterBridgeAddresses(ctx, msg)
	require.NoError(t, err)

	// 3. Verify mappings are cleaned up
	_, found = k.GetWrappedTokenContract(wctx, chainId, bridgeAddress)
	require.False(t, found)
	_, found = k.GetWrappedTokenContractByWrappedAddress(wctx, wrappedContractAddr)
	require.False(t, found)
	_, found = k.GetTokenMetadata(wctx, chainId, bridgeAddress)
	require.False(t, found)
	has = k.HasBridgeTradeApprovedToken(wctx, chainId, bridgeAddress)
	require.False(t, has)

	// 4. Verify it's registered as bridge contract
	isBridge := k.IsBridgeContractAddress(wctx, chainId, bridgeAddress)
	require.True(t, isBridge)

	// 5. Verify withdrawals fail (RequestBridgeWithdrawal)
	// Mock WASM keeper so that it passes ContractPermission check, failing ONLY at the wrapped token check
	mockWK := mockWasmKeeper{
		GetContractInfoFn: func(ctx context.Context, contractAddress sdk.AccAddress) *wasmtypes.ContractInfo {
			return &wasmtypes.ContractInfo{
				CodeID:  1,
				Creator: authority,
				Admin:   authority,
				Label:   "test",
			}
		},
	}
	msWithdraw := keeper.NewMsgServerWithWasmKeeper(k, mockWK)

	withdrawMsg := &types.MsgRequestBridgeWithdrawal{
		Creator:            wrappedContractAddr,
		UserAddress:        authority,
		Amount:             "100",
		DestinationAddress: "0xdeadbeef1234567890abcdef1234567890abcdef",
	}
	_, err = msWithdraw.RequestBridgeWithdrawal(ctx, withdrawMsg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "is not a registered wrapped token contract")
}
