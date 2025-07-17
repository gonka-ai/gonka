package keeper

import (
	"context"

	"cosmossdk.io/store/prefix"
	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/cosmos/cosmos-sdk/types/query"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/productscience/inference/x/collateral/types"
)

var _ types.QueryServer = Keeper{}

func (k Keeper) Collateral(c context.Context, req *types.QueryCollateralRequest) (*types.QueryCollateralResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}
	ctx := sdk.UnwrapSDKContext(c)

	collateral, found := k.GetCollateral(ctx, req.Participant)
	if !found {
		return nil, status.Errorf(codes.NotFound, "collateral not found for participant %s", req.Participant)
	}

	return &types.QueryCollateralResponse{Amount: collateral}, nil
}

func (k Keeper) AllCollateral(c context.Context, req *types.QueryAllCollateralRequest) (*types.QueryAllCollateralResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	var collaterals []types.CollateralBalance
	ctx := sdk.UnwrapSDKContext(c)

	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	collateralStore := prefix.NewStore(store, types.CollateralKey)

	pageRes, err := query.Paginate(collateralStore, req.Pagination, func(key []byte, value []byte) error {
		var collateral types.CollateralBalance
		collateral.Participant = string(key)
		if err := k.cdc.Unmarshal(value, &collateral.Amount); err != nil {
			return err
		}

		collaterals = append(collaterals, collateral)
		return nil
	})

	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	return &types.QueryAllCollateralResponse{Collateral: collaterals, Pagination: pageRes}, nil
}

func (k Keeper) UnbondingCollateral(c context.Context, req *types.QueryUnbondingCollateralRequest) (*types.QueryUnbondingCollateralResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}
	ctx := sdk.UnwrapSDKContext(c)

	unbondings := k.GetUnbondingByParticipant(ctx, req.Participant)

	return &types.QueryUnbondingCollateralResponse{Unbondings: unbondings}, nil
}

func (k Keeper) AllUnbondingCollateral(c context.Context, req *types.QueryAllUnbondingCollateralRequest) (*types.QueryAllUnbondingCollateralResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	var allUnbondings []types.UnbondingCollateral
	ctx := sdk.UnwrapSDKContext(c)

	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	unbondingStore := prefix.NewStore(store, types.UnbondingKey)

	pageRes, err := query.Paginate(unbondingStore, req.Pagination, func(key []byte, value []byte) error {
		var unbonding types.UnbondingCollateral
		if err := k.cdc.Unmarshal(value, &unbonding); err != nil {
			return err
		}
		allUnbondings = append(allUnbondings, unbonding)
		return nil
	})

	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	return &types.QueryAllUnbondingCollateralResponse{Unbondings: allUnbondings, Pagination: pageRes}, nil
}
