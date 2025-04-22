package keeper

import (
	"context"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/training"
	"github.com/productscience/inference/x/inference/types"
	"time"
)

func (k msgServer) JoinTraining(goCtx context.Context, msg *types.MsgJoinTraining) (*types.MsgJoinTrainingResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)
	_ = ctx

	store := NewKeeperTrainingRunStore(k.Keeper)
	runManager := training.NewRunManager(
		msg.Req.RunId,
		store,
		10,
		20,
		3*time.Minute,
		3*time.Minute,
	)

	err := runManager.Join(ctx, msg.Req.NodeId, int(msg.Req.Epoch))
	if err != nil {
		return nil, err
	}
	err = runManager.FinishIfNeeded(ctx)
	if err != nil {
		return nil, err
	}

	return &types.MsgJoinTrainingResponse{}, nil
}
