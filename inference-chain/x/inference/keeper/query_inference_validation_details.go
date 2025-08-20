package keeper

import (
	"context"

	"cosmossdk.io/collections"
	"github.com/cosmos/cosmos-sdk/types/query"
	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (k Keeper) InferenceValidationDetailsAll(ctx context.Context, req *types.QueryAllInferenceValidationDetailsRequest) (*types.QueryAllInferenceValidationDetailsResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	vals, pageRes, err := query.CollectionPaginate(
		ctx,
		k.InferenceValidationDetailsMap,
		req.Pagination,
		func(_ collections.Pair[uint64, string], v types.InferenceValidationDetails) (types.InferenceValidationDetails, error) {
			return v, nil
		},
	)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	return &types.QueryAllInferenceValidationDetailsResponse{InferenceValidationDetails: vals, Pagination: pageRes}, nil
}

func (k Keeper) InferenceValidationDetails(ctx context.Context, req *types.QueryGetInferenceValidationDetailsRequest) (*types.QueryGetInferenceValidationDetailsResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	val, found := k.GetInferenceValidationDetails(
		ctx,
		req.EpochId,
		req.InferenceId,
	)
	if !found {
		return nil, status.Error(codes.NotFound, "not found")
	}

	return &types.QueryGetInferenceValidationDetailsResponse{InferenceValidationDetails: val}, nil
}
