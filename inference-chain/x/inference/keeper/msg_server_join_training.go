package keeper

import (
	"context"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/training"
	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) JoinTraining(goCtx context.Context, msg *types.MsgJoinTraining) (*types.MsgJoinTrainingResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	store := NewKeeperTrainingRunStore(k.Keeper)
	runManager := training.NewRunManager(
		msg.Req.RunId,
		store,
		10,
		20,
	)

	err := runManager.Join(ctx, msg.Req.NodeId, msg.Req.Epoch, training.NewBlockInfo(ctx))
	if err != nil {
		k.LogError("Failed to join training", types.Training, "error", err)
		return nil, err
	}

	return &types.MsgJoinTrainingResponse{
		Status: &types.MLNodeTrainStatus{
			Status:      types.MLNodeTrainStatusEnum_OK,
			NodeId:      msg.Req.NodeId,
			Epoch:       msg.Req.Epoch,
			ActiveNodes: nil,
			Rank:        -1,
		},
	}, nil
}
