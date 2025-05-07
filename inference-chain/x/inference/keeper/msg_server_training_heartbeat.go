package keeper

import (
	"context"
	"errors"
	"github.com/productscience/inference/x/inference/training"
	"strings"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) TrainingHeartbeat(goCtx context.Context, msg *types.MsgTrainingHeartbeat) (*types.MsgTrainingHeartbeatResponse, error) {
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

	err = runManager.Heartbeat(ctx, *nodeId, msg.Req, training.NewBlockInfo(ctx))
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
