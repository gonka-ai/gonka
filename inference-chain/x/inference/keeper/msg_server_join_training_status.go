package keeper

import (
	"context"
	"github.com/productscience/inference/x/inference/types"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

func (k msgServer) JoinTrainingStatus(goCtx context.Context, msg *types.MsgJoinTrainingStatus) (*types.MsgJoinTrainingStatusResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	// TODO: Handling the message
	_ = ctx

	return &types.MsgJoinTrainingStatusResponse{}, nil
}
