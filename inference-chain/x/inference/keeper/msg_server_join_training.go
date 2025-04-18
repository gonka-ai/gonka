package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) JoinTraining(goCtx context.Context, msg *types.MsgJoinTraining) (*types.MsgJoinTrainingResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	// TODO: Handling the message
	_ = ctx

	return &types.MsgJoinTrainingResponse{}, nil
}
