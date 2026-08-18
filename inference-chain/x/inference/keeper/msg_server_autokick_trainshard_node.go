package keeper

import (
	"context"
	"fmt"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) AutokickTrainshardNode(goCtx context.Context, msg *types.MsgAutokickTrainshardNode) (*types.MsgAutokickTrainshardNodeResponse, error) {
	if err := k.CheckPermission(goCtx, msg, AccountPermission); err != nil {
		return nil, err
	}
	ctx := sdk.UnwrapSDKContext(goCtx)

	shard, err := k.Trainshards.Get(goCtx, msg.TrainshardId)
	if err != nil {
		return nil, types.ErrTrainshardNotFound.Wrapf("%d", msg.TrainshardId)
	}
	// the shard creator, its run key, or the host itself for its own node
	if msg.Creator != shard.Creator && msg.Creator != msg.Participant && (shard.RunKey == "" || msg.Creator != shard.RunKey) {
		return nil, types.ErrTrainshardAutokickNotAllowed
	}

	requestKey := collections.Join(msg.TrainshardId, msg.RequestId)
	handled, err := k.TrainshardAutokickRequest.Has(goCtx, requestKey)
	if err != nil {
		return nil, err
	}
	if handled {
		return &types.MsgAutokickTrainshardNodeResponse{}, nil
	}

	if shard.Status != types.TrainshardStatus_TRAINSHARD_STATUS_ACTIVE {
		return nil, types.ErrTrainshardNotActive.Wrapf("%d", msg.TrainshardId)
	}
	if !trainshardHasActiveNode(&shard, msg.Participant, msg.NodeId) {
		return nil, types.ErrTrainshardNodeNotActive.Wrapf("node %s of %s", msg.NodeId, msg.Participant)
	}

	height := ctx.BlockHeight()
	selected := func(n *types.TrainshardReservedNode) bool {
		return n.Participant == msg.Participant && n.NodeId == msg.NodeId
	}
	if _, err := k.releaseTrainshardNodes(goCtx, &shard, height,
		types.TrainshardNodeStatus_TRAINSHARD_NODE_STATUS_AUTOKICKED, msg.Reason, selected); err != nil {
		return nil, err
	}
	if err := k.TrainshardAutokickRequest.Set(goCtx, requestKey); err != nil {
		return nil, err
	}

	if k.hasActiveTrainshardNodes(&shard) {
		if err := k.Trainshards.Set(goCtx, shard.TrainshardId, shard); err != nil {
			return nil, err
		}
	} else if err := k.closeTrainshard(goCtx, &shard, types.TrainshardStatus_TRAINSHARD_STATUS_EXPIRED,
		types.TrainshardCloseReason_TRAINSHARD_CLOSE_REASON_NO_ACTIVE_NODES, height); err != nil {
		return nil, err
	}

	emitTrainshardEvent(goCtx, "trainshard_node_autokicked",
		sdk.NewAttribute("trainshard_id", fmt.Sprintf("%d", msg.TrainshardId)),
		sdk.NewAttribute("participant", msg.Participant),
		sdk.NewAttribute("node_id", msg.NodeId),
		sdk.NewAttribute("reason", msg.Reason),
		sdk.NewAttribute("height", fmt.Sprintf("%d", height)),
	)
	if shard.Status != types.TrainshardStatus_TRAINSHARD_STATUS_ACTIVE {
		emitTrainshardEvent(goCtx, "trainshard_expired",
			sdk.NewAttribute("trainshard_id", fmt.Sprintf("%d", msg.TrainshardId)),
			sdk.NewAttribute("creator", shard.Creator),
			sdk.NewAttribute("close_reason", shard.CloseReason.String()),
			sdk.NewAttribute("closed_at_height", fmt.Sprintf("%d", shard.ClosedAtHeight)),
		)
	}

	return &types.MsgAutokickTrainshardNodeResponse{}, nil
}
