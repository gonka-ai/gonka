package keeper

import (
	"context"
	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (k Keeper) EpochGroupValidationsAll(ctx context.Context, req *types.QueryAllEpochGroupValidationsRequest) (*types.QueryAllEpochGroupValidationsResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}
	return nil, status.Error(codes.Unimplemented, "EpochGroupValidationsAll is disabled; use EpochGroupValidations by participant and epoch")
}

func (k Keeper) EpochGroupValidations(ctx context.Context, req *types.QueryGetEpochGroupValidationsRequest) (*types.QueryGetEpochGroupValidationsResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	val, found := k.GetEpochGroupValidations(
		ctx,
		req.Participant,
		req.EpochIndex,
	)
	if !found {
		return nil, status.Error(codes.NotFound, "not found")
	}

	return &types.QueryGetEpochGroupValidationsResponse{EpochGroupValidations: val}, nil
}
