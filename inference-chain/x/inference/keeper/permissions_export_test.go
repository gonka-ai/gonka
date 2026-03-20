package keeper

import (
	"context"

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
		Keeper:     k,
		wasmKeeper: wk,
	}
}
