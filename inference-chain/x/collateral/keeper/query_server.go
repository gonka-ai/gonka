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

	participantAddr, err := sdk.AccAddressFromBech32(req.Participant)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid participant address: %v", err)
	}

	collateral, err := k.CollateralMap.Get(ctx, participantAddr)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "collateral not found for participant %s", req.Participant)
	}

	return &types.QueryCollateralResponse{Amount: collateral}, nil
}

func (k Keeper) AllCollaterals(c context.Context, req *types.QueryAllCollateralsRequest) (*types.QueryAllCollateralsResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	collaterals, pageRes, err := query.CollectionPaginate(
		c,
		k.CollateralMap,
		req.Pagination,
		func(addr sdk.AccAddress, value sdk.Coin) (types.CollateralBalance, error) {
			var collateral types.CollateralBalance
			collateral.Participant = addr.String()
			collateral.Amount = value
			return collateral, nil
		})
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	return &types.QueryAllCollateralsResponse{Collateral: collaterals, Pagination: pageRes}, nil
}

func (k Keeper) UnbondingCollateral(c context.Context, req *types.QueryUnbondingCollateralRequest) (*types.QueryUnbondingCollateralResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}
	ctx := sdk.UnwrapSDKContext(c)

	participantAddr, err := sdk.AccAddressFromBech32(req.Participant)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid participant address: %v", err)
	}

	unbondings := k.GetUnbondingByParticipant(ctx, participantAddr)

	return &types.QueryUnbondingCollateralResponse{Unbondings: unbondings}, nil
}

func (k Keeper) AllUnbondingCollaterals(c context.Context, req *types.QueryAllUnbondingCollateralsRequest) (*types.QueryAllUnbondingCollateralsResponse, error) {
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

	return &types.QueryAllUnbondingCollateralsResponse{Unbondings: allUnbondings, Pagination: pageRes}, nil
}
