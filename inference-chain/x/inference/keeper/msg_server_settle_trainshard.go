package keeper

import (
	"context"
	"fmt"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) SettleTrainshard(goCtx context.Context, msg *types.MsgSettleTrainshard) (*types.MsgSettleTrainshardResponse, error) {
	if err := k.CheckPermission(goCtx, msg, AccountPermission); err != nil {
		return nil, err
	}
	ctx := sdk.UnwrapSDKContext(goCtx)

	shard, err := k.Trainshards.Get(goCtx, msg.TrainshardId)
	if err != nil {
		return nil, types.ErrTrainshardNotFound.Wrapf("%d", msg.TrainshardId)
	}
	if shard.Creator != msg.Creator {
		return nil, types.ErrTrainshardNotCreator
	}
	if shard.Status != types.TrainshardStatus_TRAINSHARD_STATUS_ACTIVE {
		return &types.MsgSettleTrainshardResponse{}, nil
	}

	if err := k.closeTrainshard(goCtx, &shard, types.TrainshardStatus_TRAINSHARD_STATUS_SETTLED, ctx.BlockHeight()); err != nil {
		return nil, err
	}

	emitTrainshardEvent(goCtx, "trainshard_settled",
		sdk.NewAttribute("trainshard_id", fmt.Sprintf("%d", shard.TrainshardId)),
		sdk.NewAttribute("creator", shard.Creator),
		sdk.NewAttribute("closed_at_height", fmt.Sprintf("%d", shard.ClosedAtHeight)),
	)

	return &types.MsgSettleTrainshardResponse{}, nil
}
