package keeper

import (
	"context"
	"fmt"
	"slices"
	"strings"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) CreateTrainshardProposal(goCtx context.Context, msg *types.MsgCreateTrainshardProposal) (*types.MsgCreateTrainshardProposalResponse, error) {
	if err := k.CheckPermission(goCtx, msg, GovernancePermission); err != nil {
		return nil, err
	}

	params := k.GetTrainingParams(goCtx)
	if err := validateTrainshardStaticLimits(params, msg.GpuProfileId, msg.MaxNodes, msg.MaxDurationBlocks); err != nil {
		return nil, err
	}
	if _, err := sdk.AccAddressFromBech32(msg.Creator); err != nil {
		return nil, types.ErrInvalidAddress.Wrapf("creator: %s", msg.Creator)
	}

	id, err := k.nextTrainshardProposalId(goCtx)
	if err != nil {
		return nil, err
	}
	proposal := types.TrainshardProposal{
		Id:                id,
		Creator:           msg.Creator,
		GpuProfileId:      msg.GpuProfileId,
		MaxNodes:          msg.MaxNodes,
		MaxDurationBlocks: msg.MaxDurationBlocks,
		Status:            types.TrainshardProposalStatus_TRAINSHARD_PROPOSAL_STATUS_OPEN,
	}
	if err := k.TrainshardProposals.Set(goCtx, id, proposal); err != nil {
		return nil, err
	}
	if err := k.TrainshardProposalCounter.Set(goCtx, id); err != nil {
		return nil, err
	}

	emitTrainshardEvent(goCtx, "trainshard_proposal_created",
		sdk.NewAttribute("proposal_id", fmt.Sprintf("%d", id)),
		sdk.NewAttribute("creator", msg.Creator),
		sdk.NewAttribute("gpu_profile_id", msg.GpuProfileId),
	)

	return &types.MsgCreateTrainshardProposalResponse{ProposalId: id}, nil
}

func validateTrainshardStaticLimits(params *types.TrainingParams, gpuProfileId string, maxNodes uint32, maxDuration int64) error {
	if strings.TrimSpace(gpuProfileId) == "" {
		return types.ErrTrainshardProfileEmpty
	}
	if len(params.AllowedGpuProfileIds) > 0 && !slices.Contains(params.AllowedGpuProfileIds, gpuProfileId) {
		return types.ErrTrainshardProfileNotAllowed.Wrap(gpuProfileId)
	}
	if maxNodes == 0 || maxNodes > params.MaxNodesPerShard {
		return types.ErrTrainshardProposalLimits.Wrapf("max_nodes %d not in (0, %d]", maxNodes, params.MaxNodesPerShard)
	}
	if maxDuration < params.MinDurationBlocks || maxDuration > params.MaxDurationBlocks {
		return types.ErrTrainshardProposalLimits.Wrapf("max_duration_blocks %d not in [%d, %d]", maxDuration, params.MinDurationBlocks, params.MaxDurationBlocks)
	}
	return nil
}
