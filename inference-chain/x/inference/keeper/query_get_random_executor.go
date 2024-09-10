package keeper

import (
	"context"
	"fmt"
	"math/rand"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (k Keeper) GetRandomExecutor(goCtx context.Context, req *types.QueryGetRandomExecutorRequest) (*types.QueryGetRandomExecutorResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	ctx := sdk.UnwrapSDKContext(goCtx)

	activeParticipants, ok := k.GetActiveParticipants(ctx)
	if !ok {
		return nil, status.Error(codes.Internal, "Active participants not found")
	}

	if len(activeParticipants.Participants) == 0 {
		return nil, status.Error(codes.Internal, "Active participants found, but length is 0")
	}

	participantIndex := selectRandomParticipant(&activeParticipants)

	participant, ok := k.GetParticipant(ctx, participantIndex)
	if !ok {
		msg := fmt.Sprintf(
			"Selected active participant, but not found in participants list. index =  %s", participantIndex,
		)
		return nil, status.Error(codes.Internal, msg)
	}

	return &types.QueryGetRandomExecutorResponse{
		Executor: participant,
	}, nil
}

func selectRandomParticipant(participants *types.ActiveParticipants) string {
	cumulativeArray := computeCumulativeArray(participants.Participants)

	randomNumber := rand.Int63n(cumulativeArray[len(cumulativeArray)-1])
	for i, cumulativeWeight := range cumulativeArray {
		if randomNumber < cumulativeWeight {
			return participants.Participants[i].Index
		}
	}

	return participants.Participants[len(participants.Participants)-1].Index
}

func computeCumulativeArray(participants []*types.ActiveParticipant) []int64 {
	cumulativeArray := make([]int64, len(participants))
	cumulativeArray[0] = participants[0].Weight
	for i := 1; i < len(participants); i++ {
		cumulativeArray[i] = cumulativeArray[i-1] + participants[i].Weight
	}
	return cumulativeArray
}
