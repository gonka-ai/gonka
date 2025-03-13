package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
	
)

func (k msgServer) CreatePartialUpgrade(goCtx context.Context, msg *types.MsgCreatePartialUpgrade) (*types.MsgCreatePartialUpgradeResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	// TODO: Handling the message
	_ = ctx

	return &types.MsgCreatePartialUpgradeResponse{}, nil
}
