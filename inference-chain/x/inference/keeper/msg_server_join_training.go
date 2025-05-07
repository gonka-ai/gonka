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
	runManager := training.NewRunManager(msg.Req.RunId, store, k)

	err := runManager.Join(ctx, msg.Req.NodeId, msg.Req.OuterStep, training.NewBlockInfo(ctx), msg.Creator)
	if err != nil {
		k.LogError("Failed to join training", types.Training, "error", err)
		return nil, err
	}

	return &types.MsgJoinTrainingResponse{
		Status: &types.MLNodeTrainStatus{
			Status:      types.MLNodeTrainStatusEnum_OK,
			NodeId:      msg.Req.NodeId,
			OuterStep:   msg.Req.OuterStep,
			ActiveNodes: nil,
			Rank:        -1,
		},
	}, nil
}
