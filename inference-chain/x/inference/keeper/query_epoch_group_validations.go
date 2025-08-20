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

func (k Keeper) EpochGroupValidationsAll(ctx context.Context, req *types.QueryAllEpochGroupValidationsRequest) (*types.QueryAllEpochGroupValidationsResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	var epochGroupValidationss []types.EpochGroupValidations

	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	epochGroupValidationsStore := prefix.NewStore(store, types.KeyPrefix(types.EpochGroupValidationsKeyPrefix))

	pageRes, err := query.Paginate(epochGroupValidationsStore, req.Pagination, func(key []byte, value []byte) error {
		var epochGroupValidations types.EpochGroupValidations
		if err := k.cdc.Unmarshal(value, &epochGroupValidations); err != nil {
			return err
		}

		epochGroupValidationss = append(epochGroupValidationss, epochGroupValidations)
		return nil
	})

	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	return &types.QueryAllEpochGroupValidationsResponse{EpochGroupValidations: epochGroupValidationss, Pagination: pageRes}, nil
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
