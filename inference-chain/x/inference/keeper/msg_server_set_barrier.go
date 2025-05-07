package keeper

import (
	"context"
	"errors"
	"github.com/productscience/inference/x/inference/training"
	"github.com/productscience/inference/x/inference/types"
	"strings"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

func (k msgServer) SetBarrier(goCtx context.Context, msg *types.MsgSetBarrier) (*types.MsgSetBarrierResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	if !strings.HasPrefix(msg.Req.NodeId, msg.Creator+"/") {
		return nil, errors.New("nodeId must start with creator")
	}
	nodeId, err := training.NewGlobalNodeId(msg.Req.NodeId)
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
