package keeper

import (
	"context"

	"github.com/productscience/inference/x/inference/training"
	"github.com/productscience/inference/x/inference/types"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

func (k msgServer) SetBarrier(goCtx context.Context, msg *types.MsgSetBarrier) (*types.MsgSetBarrierResponse, error) {
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

	barrier := &types.TrainingTaskBarrier{
		BarrierId:   msg.Req.BarrierId,
		TaskId:      msg.Req.RunId,
		Participant: nodeId.Participant,
		NodeId:      nodeId.LocalNodeId,
		OuterStep:   msg.Req.OuterStep,
		BlockHeight: ctx.BlockHeight(),
		BlockTime:   ctx.BlockTime().UnixMilli(),
	}
	runManager.SetBarrier(ctx, barrier)

	resp := &types.SetBarrierResponse{
		Status: types.BarrierStatusEnum_READY,
	}

	return &types.MsgSetBarrierResponse{
		Resp: resp,
	}, nil
}
