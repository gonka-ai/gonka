package keeper

import (
	"context"
	"github.com/productscience/inference/x/inference/training"
	"github.com/productscience/inference/x/inference/types"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

func (k msgServer) JoinTrainingStatus(goCtx context.Context, msg *types.MsgJoinTrainingStatus) (*types.MsgJoinTrainingStatusResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	runManager := training.NewRunManager(msg.Req.RunId, NewKeeperTrainingRunStore(k.Keeper))
	status, err := runManager.JoinStatus(ctx, msg.Req.NodeId, msg.Req.Epoch, training.NewBlockInfo(ctx), msg.Creator)
	if err != nil {
		k.LogError("Failed to get join training status", types.Training, "error", err)
		return nil, err
	}

	return &types.MsgJoinTrainingStatusResponse{
		Status: status,
	}, nil
}
