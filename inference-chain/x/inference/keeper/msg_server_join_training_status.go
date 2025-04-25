package keeper

import (
	"context"
	"github.com/productscience/inference/x/inference/training"
	"github.com/productscience/inference/x/inference/types"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

func (k msgServer) JoinTrainingStatus(goCtx context.Context, msg *types.MsgJoinTrainingStatus) (*types.MsgJoinTrainingStatusResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	runManager := training.NewRunManager(msg.Req.RunId, NewKeeperTrainingRunStore(k.Keeper), 10, 10)
	_ = runManager
	_ = ctx

	return &types.MsgJoinTrainingStatusResponse{}, nil
}
