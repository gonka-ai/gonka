package keeper_test

import (
	"context"
	"testing"

	wasmtypes "github.com/CosmWasm/wasmd/x/wasm/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

// Mock WasmKeeper for testing
type mockWasmKeeper struct {
	GetContractInfoFn func(ctx context.Context, contractAddress sdk.AccAddress) *wasmtypes.ContractInfo
}

func (m mockWasmKeeper) GetContractInfo(ctx context.Context, contractAddress sdk.AccAddress) *wasmtypes.ContractInfo {
	if m.GetContractInfoFn != nil {
		return m.GetContractInfoFn(ctx, contractAddress)
	}
	return nil
}

func TestMsgServer_RequestBridgeWithdrawal_Permissions(t *testing.T) {
	// Use existing setup to get the base keeper
	keep, _, ctx, _ := setupKeeperWithMocks(t)
	wctx := sdk.UnwrapSDKContext(ctx)

	// Create a valid address
	signerAddr := sdk.AccAddress([]byte("contract_addr______"))
	signer := signerAddr.String()

	msg := &types.MsgRequestBridgeWithdrawal{
		Creator: signer,
	}

	// 1. Test: Signer is NOT a contract -> Should fail ContractPermission check
	// Mock returns nil
	mockWK := mockWasmKeeper{
		GetContractInfoFn: func(ctx context.Context, contractAddress sdk.AccAddress) *wasmtypes.ContractInfo {
			return nil
		},
	}

	// Create MsgServer with our mock
	ms := keeper.NewMsgServerWithWasmKeeper(keep, mockWK)

	err := keeper.CheckPermission(ms, wctx, msg, keeper.ContractPermission)
	require.ErrorIs(t, err, types.ErrNotAContractAddress)

	// 2. Test: Signer IS a contract -> Should pass ContractPermission check
	mockWKPassed := mockWasmKeeper{
		GetContractInfoFn: func(ctx context.Context, contractAddress sdk.AccAddress) *wasmtypes.ContractInfo {
			return &wasmtypes.ContractInfo{
				CodeID:  1,
				Creator: signer,
				Admin:   signer,
				Label:   "test",
			}
		},
	}

	msPassed := keeper.NewMsgServerWithWasmKeeper(keep, mockWKPassed)

	err = keeper.CheckPermission(msPassed, wctx, msg, keeper.ContractPermission)
	require.NoError(t, err)
}
