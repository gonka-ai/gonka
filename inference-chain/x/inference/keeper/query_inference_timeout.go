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

func (k Keeper) InferenceTimeoutAll(ctx context.Context, req *types.QueryAllInferenceTimeoutRequest) (*types.QueryAllInferenceTimeoutResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	var inferenceTimeouts []types.InferenceTimeout

	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	inferenceTimeoutStore := prefix.NewStore(store, types.KeyPrefix(types.InferenceTimeoutKeyPrefix))

	pageRes, err := query.Paginate(inferenceTimeoutStore, req.Pagination, func(key []byte, value []byte) error {
		var inferenceTimeout types.InferenceTimeout
		if err := k.cdc.Unmarshal(value, &inferenceTimeout); err != nil {
			return err
		}

		inferenceTimeouts = append(inferenceTimeouts, inferenceTimeout)
		return nil
	})

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
