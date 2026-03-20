package keeper

import (
	"github.com/productscience/inference/x/inference/types"
)

type msgServer struct {
	Keeper
	wasmKeeper types.WasmKeeper
}

// NewMsgServerImpl returns an implementation of the MsgServer interface
// for the provided Keeper.
func NewMsgServerImpl(keeper Keeper) types.MsgServer {
	return &msgServer{
		Keeper:     keeper,
		wasmKeeper: keeper.GetWasmKeeper(),
	}
}

var _ types.MsgServer = msgServer{}
