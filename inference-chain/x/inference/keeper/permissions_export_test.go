package keeper

import (
	"context"

	wasmkeeper "github.com/CosmWasm/wasmd/x/wasm/keeper"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

// CheckPermission exposes the unexported msgServer's CheckPermission method for tests
func CheckPermission(ms types.MsgServer, ctx context.Context, msg HasSigners, permission Permission, permissions ...Permission) error {
	// msgServer is defined in msg_server.go as: type msgServer struct { Keeper }
	// NewMsgServerImpl returns pointer to it: return &msgServer{Keeper: keeper}
	server, ok := ms.(*msgServer)
	if !ok {
		panic("MsgServer is not the expected internal implementation")
	}
	return server.CheckPermission(ctx, msg, permission, permissions...)
}

// NewMsgServerWithWasmKeeper creates a MsgServer with a custom WasmKeeper for testing
func NewMsgServerWithWasmKeeper(k Keeper, wk types.WasmKeeper) types.MsgServer {
	return &msgServer{
		Keeper:             k,
		contractInfoLookup: wk.GetContractInfo,
	}
}

// SetMintTokensFnForTesting injects mint callback for keeper tests only.
func (k *Keeper) SetMintTokensFnForTesting(mintTokensFn func(ctx sdk.Context, contractAddr, recipient, amount string) error) {
	k.mintTokensFn = mintTokensFn
}

// NewMsgServerWithWasmKeeperGetter creates a MsgServer that exercises the
// GetWasmKeeper() production path, NOT the contractInfoLookup shortcut.
// This reproduces the exact code path that caused the production panic in the
// bridge unwrap flow: CW20 contract → MsgRequestBridgeWithdrawal →
// CheckPermission(ContractPermission) → checkContractPermission →
// k.GetWasmKeeper().GetContractInfo.
func NewMsgServerWithWasmKeeperGetter(k Keeper, getterFn func() Keeper) types.MsgServer {
	// If a getter override is provided, swap the keeper's internal getter so
	// GetWasmKeeper() on the returned msgServer uses it.
	if getterFn != nil {
		k.SetWasmKeeperGetter(func() wasmkeeper.Keeper {
			return getterFn().GetWasmKeeper()
		})
	}
	// contractInfoLookup is intentionally left nil so checkContractPermission
	// falls through to the GetWasmKeeper() path.
	return &msgServer{Keeper: k}
}
