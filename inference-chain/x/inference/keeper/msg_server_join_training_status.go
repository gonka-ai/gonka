package keeper

import (
	"context"
	"errors"
	"github.com/productscience/inference/x/inference/training"
	"github.com/productscience/inference/x/inference/types"
	"strings"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

func (k msgServer) JoinTrainingStatus(goCtx context.Context, msg *types.MsgJoinTrainingStatus) (*types.MsgJoinTrainingStatusResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	if !strings.HasPrefix(msg.Req.NodeId, msg.Creator+"/") {
		return nil, errors.New("nodeId must start with creator")
	}
	nodeId, err := training.NewGlobalNodeId(msg.Req.NodeId)
	if err != nil {
		return nil, err
	}

	runManager := training.NewRunManager(msg.Req.RunId, NewKeeperTrainingRunStore(k.Keeper), k)
	status, err := runManager.JoinStatus(ctx, *nodeId, msg.Req.OuterStep, training.NewBlockInfo(ctx))
	if err != nil {
		k.LogError("Failed to get join training status", types.Training, "error", err)
		return nil, err
	}

	return &types.MsgJoinTrainingStatusResponse{
		Status: status,
	}, nil
}
