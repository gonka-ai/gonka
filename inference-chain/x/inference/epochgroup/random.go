package epochgroup

import (
	"context"
	"fmt"
	"math/rand"
	"strconv"

	"github.com/cosmos/cosmos-sdk/x/group"
	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// GetRandomMemberForModel gets a random member for a specific model
func (eg *EpochGroup) GetRandomMemberForModel(
	goCtx context.Context,
	modelId string,
	filterFn func([]*group.GroupMember) []*group.GroupMember,
) (*types.Participant, error) {
	// If modelId is provided and this is the parent group, delegate to the sub-group
	if modelId != "" && eg.GroupData.GetModelId() == "" {
		subGroup, err := eg.GetSubGroup(goCtx, modelId)
		if err != nil {
			return nil, status.Error(codes.Internal, fmt.Sprintf("Error getting sub-group for model %s: %v", modelId, err))
		}
		return subGroup.GetRandomMember(goCtx, filterFn)
	}

	// Otherwise, get a random member from this group
	return eg.GetRandomMember(goCtx, filterFn)
}

func (eg *EpochGroup) GetRandomMember(
	goCtx context.Context,
	filterFn func([]*group.GroupMember) []*group.GroupMember,
) (*types.Participant, error) {
	// Use the context as is, don't try to unwrap it
	// This allows the method to work with both SDK contexts and regular contexts
	ctx := goCtx

	activeParticipants, err := eg.getAllGroupMembersPaginated(ctx, uint64(eg.GroupData.EpochGroupId))
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	activeParticipants = sanitizeMembers(activeParticipants)
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
	if participant == nil || participant.Member == nil {
		return 0
	}
	weight, err := strconv.Atoi(participant.Member.Weight)
	if err != nil {
		return 0
	}
	return int64(weight)
}

func sanitizeMembers(members []*group.GroupMember) []*group.GroupMember {
	if len(members) == 0 {
		return members
	}
	filtered := make([]*group.GroupMember, 0, len(members))
	for _, member := range members {
		if member == nil || member.Member == nil {
			continue
		}
		filtered = append(filtered, member)
	}
	return filtered
}
