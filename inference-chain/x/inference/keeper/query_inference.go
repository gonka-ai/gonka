package keeper

import (
	"context"

	"cosmossdk.io/store/prefix"
	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/cosmos/cosmos-sdk/types/query"
	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (k Keeper) InferenceAll(ctx context.Context, req *types.QueryAllInferenceRequest) (*types.QueryAllInferenceResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	var inferences []types.Inference

	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	inferenceStore := prefix.NewStore(store, types.KeyPrefix(types.InferenceKeyPrefix))

	pageRes, err := query.Paginate(inferenceStore, req.Pagination, func(key []byte, value []byte) error {
		var inference types.Inference
		if err := k.cdc.Unmarshal(value, &inference); err != nil {
			return err
		}

		inferences = append(inferences, inference)
		return nil
	})

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
