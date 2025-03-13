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

func (k Keeper) PartialUpgradeAll(ctx context.Context, req *types.QueryAllPartialUpgradeRequest) (*types.QueryAllPartialUpgradeResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	var partialUpgrades []types.PartialUpgrade

	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	partialUpgradeStore := prefix.NewStore(store, types.KeyPrefix(types.PartialUpgradeKeyPrefix))

	pageRes, err := query.Paginate(partialUpgradeStore, req.Pagination, func(key []byte, value []byte) error {
		var partialUpgrade types.PartialUpgrade
		if err := k.cdc.Unmarshal(value, &partialUpgrade); err != nil {
			return err
		}

		partialUpgrades = append(partialUpgrades, partialUpgrade)
		return nil
	})

	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	return &types.QueryAllPartialUpgradeResponse{PartialUpgrade: partialUpgrades, Pagination: pageRes}, nil
}

func (k Keeper) PartialUpgrade(ctx context.Context, req *types.QueryGetPartialUpgradeRequest) (*types.QueryGetPartialUpgradeResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	val, found := k.GetPartialUpgrade(
		ctx,
		req.Height,
	)
	if !found {
		return nil, status.Error(codes.NotFound, "not found")
	}

	return &types.QueryGetPartialUpgradeResponse{PartialUpgrade: val}, nil
}
