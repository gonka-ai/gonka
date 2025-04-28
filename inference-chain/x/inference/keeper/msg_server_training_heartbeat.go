package keeper

import (
	"context"
	"github.com/productscience/inference/x/inference/training"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) TrainingHeartbeat(goCtx context.Context, msg *types.MsgTrainingHeartbeat) (*types.MsgTrainingHeartbeatResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	store := NewKeeperTrainingRunStore(k.Keeper)
	runManager := training.NewRunManager(msg.Req.RunId, store, k)

	err := runManager.Heartbeat(ctx, msg.Creator, msg.Req.NodeId, msg.Req.GlobalEpoch, training.NewBlockInfo(ctx))
	if err != nil {
		k.LogError("Failed to send heartbeat", types.Training, "error", err)
		return &types.MsgTrainingHeartbeatResponse{
			Resp: &types.HeartbeatResponse{
				Status: types.HeartbeatStatusEnum_HEARTBEAT_ERROR,
			},
		}, err // TODO: should we return both error resp and error body?
	}

	return &types.MsgTrainingHeartbeatResponse{
		Resp: &types.HeartbeatResponse{
			Status: types.HeartbeatStatusEnum_HEARTBEAT_OK,
		},
	}, nil
}
