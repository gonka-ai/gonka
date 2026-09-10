package keeper

import (
	"context"
	"fmt"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) AssembleTrainshard(goCtx context.Context, msg *types.MsgAssembleTrainshard) (*types.MsgAssembleTrainshardResponse, error) {
	if err := k.CheckPermission(goCtx, msg, AccountPermission); err != nil {
		return nil, err
	}
	ctx := sdk.UnwrapSDKContext(goCtx)

	params := k.GetTrainingParams(goCtx)
	if !params.TrainingEnabled {
		return nil, types.ErrTrainingDisabled
	}

	proposal, err := k.TrainshardProposals.Get(goCtx, msg.ProposalId)
	if err != nil {
		return nil, types.ErrTrainshardProposalNotFound.Wrapf("%d", msg.ProposalId)
	}
	if proposal.Status != types.TrainshardProposalStatus_TRAINSHARD_PROPOSAL_STATUS_OPEN {
		return nil, types.ErrTrainshardProposalNotOpen.Wrapf("%d", msg.ProposalId)
	}
	if proposal.Creator != msg.Creator {
		return nil, types.ErrTrainshardNotCreator
	}
	// governance may have tightened the limits after the vote passed
	if err := validateTrainshardStaticLimits(params, proposal.GpuProfileId, proposal.MaxNodes, proposal.MaxDurationBlocks); err != nil {
		return nil, err
	}

	height := ctx.BlockHeight()
	if until := k.creatorCooldownUntil(goCtx, msg.Creator); height < until {
		return nil, types.ErrTrainshardCooldown.Wrapf("until height %d", until)
	}

	reserved, err := k.buildTrainingReservedCounts(goCtx)
	if err != nil {
		return nil, err
	}
	if reserved.activeShards >= int(params.MaxActiveShards) {
		return nil, types.ErrTrainshardActiveLimit.Wrapf("global active shards %d", reserved.activeShards)
	}
	if reserved.perCreatorAct[msg.Creator] >= int(params.MaxActiveShardsPerCreator) {
		return nil, types.ErrTrainshardActiveLimit.Wrapf("creator active shards %d", reserved.perCreatorAct[msg.Creator])
	}

	trainshardId, err := k.nextTrainshardId(goCtx)
	if err != nil {
		return nil, err
	}

	nodes, err := k.selectTrainshardNodes(goCtx, trainshardId, proposal.GpuProfileId, proposal.MaxNodes, params)
	if err != nil {
		return nil, err
	}

	shard := types.Trainshard{
		TrainshardId:    trainshardId,
		Creator:         msg.Creator,
		GpuProfileId:    proposal.GpuProfileId,
		Status:          types.TrainshardStatus_TRAINSHARD_STATUS_ACTIVE,
		CreatedAtHeight: height,
		ExpiresAtHeight: height + proposal.MaxDurationBlocks,
		Nodes:           nodes,
		ProposalId:      proposal.Id,
		BaseImage:       proposal.BaseImage,
		RunKey:          proposal.RunKey,
	}
	if err := k.reserveTrainshardNodes(goCtx, &shard); err != nil {
		return nil, err
	}
	if err := k.TrainshardCounter.Set(goCtx, trainshardId); err != nil {
		return nil, err
	}

	proposal.Status = types.TrainshardProposalStatus_TRAINSHARD_PROPOSAL_STATUS_CONSUMED
	if err := k.TrainshardProposals.Set(goCtx, proposal.Id, proposal); err != nil {
		return nil, err
	}

	emitTrainshardEvent(goCtx, "trainshard_assembled",
		sdk.NewAttribute("trainshard_id", fmt.Sprintf("%d", trainshardId)),
		sdk.NewAttribute("creator", msg.Creator),
		sdk.NewAttribute("gpu_profile_id", proposal.GpuProfileId),
		sdk.NewAttribute("expires_at_height", fmt.Sprintf("%d", shard.ExpiresAtHeight)),
		sdk.NewAttribute("base_image", proposal.BaseImage),
	)
	reservedNodes := make(map[string]bool)
	for _, n := range nodes {
		key := nodeKey(n.Participant, n.NodeId)
		if reservedNodes[key] {
			continue
		}
		reservedNodes[key] = true
		emitTrainshardEvent(goCtx, "trainshard_node_reserved",
			sdk.NewAttribute("trainshard_id", fmt.Sprintf("%d", trainshardId)),
			sdk.NewAttribute("participant", n.Participant),
			sdk.NewAttribute("node_id", n.NodeId),
		)
	}

	return &types.MsgAssembleTrainshardResponse{TrainshardId: trainshardId}, nil
}
