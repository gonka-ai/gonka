package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/cosmos/cosmos-sdk/types/query"
	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (k Keeper) ParticipantAll(ctx context.Context, req *types.QueryAllParticipantRequest) (*types.QueryAllParticipantResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	participants, pageRes, err := query.CollectionPaginate(
		ctx,
		k.Participants,
		req.Pagination,
		func(_ sdk.AccAddress, value types.Participant) (types.Participant, error) {
			return value, nil
		},
	)

	return &types.QueryAllParticipantResponse{Participant: participants, Pagination: pageRes, BlockHeight: sdkCtx.BlockHeight()}, err
}

func (k Keeper) Participant(ctx context.Context, req *types.QueryGetParticipantRequest) (*types.QueryGetParticipantResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	val, found := k.GetParticipant(
		ctx,
		req.Index,
	)
	if !found {
		return nil, status.Error(codes.NotFound, "not found")
	}

	return &types.QueryGetParticipantResponse{Participant: val}, nil
}
