package keeper

import (
	"context"

	"github.com/cosmos/cosmos-sdk/types/query"
	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (k Keeper) InferenceAll(ctx context.Context, req *types.QueryAllInferenceRequest) (*types.QueryAllInferenceResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	inferences, pageRes, err := query.CollectionPaginate(
		ctx,
		k.Inferences,
		req.Pagination,
		func(_ string, v types.Inference) (types.Inference, error) { return v, nil },
	)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &types.QueryAllInferenceResponse{Inference: inferences, Pagination: pageRes}, nil
}

func (k Keeper) Inference(ctx context.Context, req *types.QueryGetInferenceRequest) (*types.QueryGetInferenceResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	val, found := k.GetInference(
		ctx,
		req.Index,
	)
	if !found {
		return nil, status.Error(codes.NotFound, "not found")
	}

	return &types.QueryGetInferenceResponse{Inference: val}, nil
}
