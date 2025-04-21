package keeper

import (
	"context"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) JoinTraining(goCtx context.Context, msg *types.MsgJoinTraining) (*types.MsgJoinTrainingResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)
	_ = ctx

	//trainingRunManager := training.NewRunManager(msg)

	//trainingRunManager.Join(ctx, )
	//trainingRunManager.FinishIfNeeded()

	return &types.MsgJoinTrainingResponse{}, nil
}
