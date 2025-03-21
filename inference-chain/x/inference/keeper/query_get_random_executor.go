package keeper

import (
	"context"
	"github.com/cosmos/cosmos-sdk/x/group"
	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"slices"
)

func (k Keeper) GetRandomExecutor(goCtx context.Context, req *types.QueryGetRandomExecutorRequest) (*types.QueryGetRandomExecutorResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	epochGroup, err := k.GetCurrentEpochGroup(goCtx)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	k.GetAllHardwareNodes(goCtx)
	allParticipants := k.GetAllParticipant(goCtx)
	participantsById := make(map[string]*types.Participant)
	for _, participant := range allParticipants {
		participantsById[participant.Address] = &participant
	}

	participant, err := epochGroup.GetRandomMember(goCtx, func(members []*group.GroupMember) []*group.GroupMember {
		filteredMembers := make([]*group.GroupMember, 0)
		for _, member := range members {
			participant, ok := participantsById[member.Member.Address]
			if !ok || participant == nil {
				continue
			}

			if slices.Contains(participant.Models, req.Model) {
				filteredMembers = append(filteredMembers, member)
			}
		}

		return filteredMembers
	})
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	return &types.QueryGetRandomExecutorResponse{
		Executor: *participant,
	}, nil
}
