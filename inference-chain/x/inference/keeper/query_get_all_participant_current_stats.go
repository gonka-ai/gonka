package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (k Keeper) GetAllParticipantCurrentStats(goCtx context.Context, req *types.QueryGetAllParticipantCurrentStatsRequest) (*types.QueryGetAllParticipantCurrentStatsResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	ctx := sdk.UnwrapSDKContext(goCtx)

	currentEpoch, err := k.GetCurrentEpochGroup(ctx)
	if err != nil {
		k.LogError("GetParticipantCurrentStats failure", types.Participants, "error", err)
		return nil, status.Error(codes.Internal, err.Error())
	}
	response := &types.QueryGetAllParticipantCurrentStatsResponse{
		BlockHeight: ctx.BlockHeight(),
		EpochId:     int64(currentEpoch.GroupData.EpochGroupId),
	}

	for _, weight := range currentEpoch.GroupData.ValidationWeights {
		newParticipantCurrentStats := &types.ParticipantCurrentStats{
			ParticipantId: weight.MemberAddress,
			Weight:        uint64(weight.Weight),
			Reputation:    weight.Reputation,
		}
		response.ParticipantCurrentStats = append(response.ParticipantCurrentStats, newParticipantCurrentStats)
	}
	return response, nil
}
