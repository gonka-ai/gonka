package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/training"
	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) JoinTraining(goCtx context.Context, msg *types.MsgJoinTraining) (*types.MsgJoinTrainingResponse, error) {
	if err := k.CheckPermission(goCtx, msg, TrainingExecPermission); err != nil {
		return nil, err
	}

	ctx := sdk.UnwrapSDKContext(goCtx)
	nodeId, err := training.NewGlobalNodeId(msg.Req.NodeId, msg.Creator)
	if err != nil {
		return nil, err
	}

	store := NewKeeperTrainingRunStore(k.Keeper)
	runManager := training.NewRunManager(msg.Req.RunId, store, k)
	err = runManager.Join(ctx, *nodeId, msg.Req.OuterStep, training.NewBlockInfo(ctx))
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
