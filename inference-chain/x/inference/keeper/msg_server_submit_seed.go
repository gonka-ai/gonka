package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) SubmitSeed(goCtx context.Context, msg *types.MsgSubmitSeed) (*types.MsgSubmitSeedResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)
	// TODO: Handling the message
	_ = ctx

	return &types.MsgSubmitSeedResponse{}, nil
}
