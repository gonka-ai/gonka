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
	runManager := training.NewRunManager(
		msg.Req.RunId,
		store,
		10,
		20,
	)

	err := runManager.Heartbeat(ctx, msg.Req.NodeId, msg.Req.GlobalEpoch)
	if err != nil {
		k.LogError("Failed to send heartbeat", types.Training, "error", err)
		return nil, err
	}

	// PRTODO: add response!
	return &types.MsgTrainingHeartbeatResponse{}, nil
}
