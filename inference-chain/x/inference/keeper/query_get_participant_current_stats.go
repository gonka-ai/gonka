package keeper

import (
	"context"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (k Keeper) GetParticipantCurrentStats(goCtx context.Context, req *types.QueryGetParticipantCurrentStatsRequest) (*types.QueryGetParticipantCurrentStatsResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	ctx := sdk.UnwrapSDKContext(goCtx)

	currentEpoch, err := k.GetCurrentEpochGroup(ctx)
	if err != nil {
		k.LogError("GetParticipantCurrentStats failure", types.Participants, "error", err)
		return nil, status.Error(codes.Internal, err.Error())
	}
	response := &types.QueryGetParticipantCurrentStatsResponse{}
	for _, weight := range currentEpoch.GroupData.ValidationWeights {
		if weight.MemberAddress == req.ParticipantId {
			response.Weight = uint64(weight.Weight)
			response.Reputation = weight.Reputation
		}
	}

	return response, nil
}
