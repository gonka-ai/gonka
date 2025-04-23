package keeper

import (
	"context"
	"github.com/productscience/inference/x/inference/training"
	"github.com/productscience/inference/x/inference/types"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

func (k msgServer) SetBarrier(goCtx context.Context, msg *types.MsgSetBarrier) (*types.MsgSetBarrierResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	store := NewKeeperTrainingRunStore(k.Keeper)
	runManager := training.NewRunManager(
		msg.Req.RunId,
		store,
		10,
		10,
	)

	barrier := &types.TrainingTaskBarrier{
		BarrierId:   msg.Req.BarrierId,
		TaskId:      msg.Req.RunId,
		Participant: msg.Creator,
		NodeId:      msg.Req.NodeId,
		Epoch:       msg.Req.Epoch,
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
