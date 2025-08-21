package keeper

import (
	"context"

	"cosmossdk.io/collections"
	"github.com/cosmos/cosmos-sdk/types/query"
	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (k Keeper) InferenceTimeoutAll(ctx context.Context, req *types.QueryAllInferenceTimeoutRequest) (*types.QueryAllInferenceTimeoutResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	inferenceTimeouts, pageRes, err := query.CollectionPaginate(
		ctx,
		k.InferenceTimeouts,
		req.Pagination,
		func(_ collections.Pair[uint64, string], v types.InferenceTimeout) (types.InferenceTimeout, error) {
			return v, nil
		},
	)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &types.QueryAllInferenceTimeoutResponse{InferenceTimeout: inferenceTimeouts, Pagination: pageRes}, nil
}

func (k Keeper) InferenceTimeout(ctx context.Context, req *types.QueryGetInferenceTimeoutRequest) (*types.QueryGetInferenceTimeoutResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	val, found := k.GetInferenceTimeout(
		ctx,
		req.ExpirationHeight,
		req.InferenceId,
	)
	if !found {
		return nil, status.Error(codes.NotFound, "not found")
	}

	return &types.QueryGetInferenceTimeoutResponse{InferenceTimeout: val}, nil
}
