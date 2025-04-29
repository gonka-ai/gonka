package epochgroup

import (
	"context"
	"fmt"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/group"
	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"math/rand"
	"strconv"
)

func (eg *EpochGroup) GetRandomMember(
	goCtx context.Context,
	filterFn func([]*group.GroupMember) []*group.GroupMember,
	modelId string,
) (*types.Participant, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	// Use the model-specific EpochGroup if available
	groupId := eg.GroupData.EpochGroupId
	if modelId != "" {
		// Find the ModelEpochGroup for this model
		var modelEpochGroup *types.ModelEpochGroup
		for _, meg := range eg.GroupData.ModelEpochGroups {
			if meg.ModelId == modelId {
				modelEpochGroup = meg
				break
			}
		}

		// If we found a ModelEpochGroup for this model, use its group ID
		if modelEpochGroup != nil {
			groupId = modelEpochGroup.EpochGroupId
			eg.Logger.LogInfo("Using model-specific epoch group for random member selection", types.EpochGroup, "model", modelId, "groupId", groupId)
		}
	}

	groupMemberResponse, err := eg.GroupKeeper.GroupMembers(ctx, &group.QueryGroupMembersRequest{GroupId: uint64(groupId)})
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	activeParticipants := groupMemberResponse.GetMembers()
	if len(activeParticipants) == 0 {
		return nil, status.Error(codes.Internal, "Active participants found, but length is 0")
	}

	filteredParticipants := filterFn(activeParticipants)
	if len(filteredParticipants) == 0 {
		return nil, status.Error(codes.Internal, "After filtering participants the length is 0")
	}

	participantIndex := selectRandomParticipant(filteredParticipants)

	participant, ok := eg.ParticipantKeeper.GetParticipant(ctx, participantIndex)
	if !ok {
		msg := fmt.Sprintf(
			"Selected active participant, but not found in participants list. index =  %s", participantIndex,
		)
		return nil, status.Error(codes.Internal, msg)
	}
	return &participant, nil
}

func selectRandomParticipant(participants []*group.GroupMember) string {
	cumulativeArray := computeCumulativeArray(participants)

	randomNumber := rand.Int63n(cumulativeArray[len(cumulativeArray)-1])
	for i, cumulativeWeight := range cumulativeArray {
		if randomNumber < cumulativeWeight {
			return participants[i].Member.Address
		}
	}

	return participants[len(participants)-1].Member.Address
}

func computeCumulativeArray(participants []*group.GroupMember) []int64 {
	cumulativeArray := make([]int64, len(participants))
	cumulativeArray[0] = int64(getWeight(participants[0]))
	for i := 1; i < len(participants); i++ {
		cumulativeArray[i] = cumulativeArray[i-1] + getWeight(participants[i])
	}
	return cumulativeArray
}

func getWeight(participant *group.GroupMember) int64 {
	weight, err := strconv.Atoi(participant.Member.Weight)
	if err != nil {
		return 0
	}
	return int64(weight)
}
