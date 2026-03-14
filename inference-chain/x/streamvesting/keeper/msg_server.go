package keeper

import (
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"

	"github.com/productscience/inference/x/streamvesting/types"
)

type msgServer struct {
	Keeper
}

// NewMsgServerImpl returns an implementation of the MsgServer interface
// for the provided Keeper.
func NewMsgServerImpl(keeper Keeper) types.MsgServer {
	return &msgServer{Keeper: keeper}
}

var _ types.MsgServer = msgServer{}

// isAllowedVestingSender returns true if sender is the governance authority
// or the inference module account.
func (k msgServer) isAllowedVestingSender(sender string) bool {
	return sender == k.GetAuthority() ||
		sender == authtypes.NewModuleAddress("inference").String()
}
