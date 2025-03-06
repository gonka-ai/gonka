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

func (k Keeper) InferenceValidationDetailsAll(ctx context.Context, req *types.QueryAllInferenceValidationDetailsRequest) (*types.QueryAllInferenceValidationDetailsResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	var inferenceValidationDetailss []types.InferenceValidationDetails

	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	inferenceValidationDetailsStore := prefix.NewStore(store, types.KeyPrefix(types.InferenceValidationDetailsKeyPrefix))

	pageRes, err := query.Paginate(inferenceValidationDetailsStore, req.Pagination, func(key []byte, value []byte) error {
		var inferenceValidationDetails types.InferenceValidationDetails
		if err := k.cdc.Unmarshal(value, &inferenceValidationDetails); err != nil {
			return err
		}

		inferenceValidationDetailss = append(inferenceValidationDetailss, inferenceValidationDetails)
		return nil
	})

	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	return &types.QueryAllInferenceValidationDetailsResponse{InferenceValidationDetails: inferenceValidationDetailss, Pagination: pageRes}, nil
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
