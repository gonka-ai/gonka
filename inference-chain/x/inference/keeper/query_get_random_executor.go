package keeper

import (
	"context"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/group"
	"github.com/productscience/inference/x/inference/epochgroup"
	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"strings"
)

func (k Keeper) GetRandomExecutor(goCtx context.Context, req *types.QueryGetRandomExecutorRequest) (*types.QueryGetRandomExecutorResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	epochGroup, err := k.GetCurrentEpochGroup(goCtx)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	// If a model is specified, use the GetRandomMemberForModel method
	if strings.TrimSpace(req.Model) != "" {
		k.LogInfo("GetRandomExecutor: using model-specific group", types.Inferences, "model", req.Model)
		participant, err := epochGroup.GetRandomMemberForModel(goCtx, req.Model, func(members []*group.GroupMember) []*group.GroupMember {
			return members // No additional filtering needed, as we're already using the model-specific group
		})
		if err != nil {
			return nil, status.Error(codes.Internal, err.Error())
		}

		return &types.QueryGetRandomExecutorResponse{
			Executor: *participant,
		}, nil
	}

	// If no model is specified, use the original implementation
	participantSetByModel, err := k.getParticipantSetByModel(goCtx, epochGroup)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	k.LogInfo("Participant set by model", types.Inferences, "participantSetByModel", participantSetByModel)

	participant, err := epochGroup.GetRandomMember(goCtx, func(members []*group.GroupMember) []*group.GroupMember {
		k.LogInfo("GetRandomExecutor: model is empty, returning all members", types.Inferences)
		return members
	})
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	return &types.QueryGetRandomExecutorResponse{
		Executor: *participant,
	}, nil
}

func (k Keeper) getParticipantSetByModel(goCtx context.Context, epochGroup *epochgroup.EpochGroup) (map[string]map[string]bool, error) {
	groupMemberResponse, err := epochGroup.GroupKeeper.GroupMembers(goCtx, &group.QueryGroupMembersRequest{GroupId: uint64(epochGroup.GroupData.EpochGroupId)})
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	activeParticipants := groupMemberResponse.GetMembers()
	activeParticipantIds := make([]string, len(activeParticipants))
	for i, activeParticipant := range activeParticipants {
		activeParticipantIds[i] = activeParticipant.Member.Address
	}

	sdkCtx := sdk.UnwrapSDKContext(goCtx)
	hardwareNodes, err := k.GetHardwareNodesForParticipants(sdkCtx, activeParticipantIds)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	participantSetByModel := make(map[string]map[string]bool)
	for _, participantNodes := range hardwareNodes {
		participantId := participantNodes.Participant
		for _, node := range participantNodes.HardwareNodes {
			for _, model := range node.Models {
				if participantSetByModel[model] == nil {
					participantSetByModel[model] = make(map[string]bool)
				}
				participantSetByModel[model][participantId] = true
			}
		}
	}

	return participantSetByModel, nil
}
