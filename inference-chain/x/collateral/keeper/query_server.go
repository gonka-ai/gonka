package keeper

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/productscience/inference/x/collateral/types"
)

var _ types.QueryServer = Keeper{}

// Params queries the parameters of the module.
func (k Keeper) Params(ctx context.Context, req *types.QueryParamsRequest) (*types.QueryParamsResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	params := k.GetParams(sdkCtx)

	return &types.QueryParamsResponse{Params: params}, nil
}

// ParticipantCollateral queries the collateral of a specific participant.
func (k Keeper) ParticipantCollateral(ctx context.Context, req *types.QueryParticipantCollateralRequest) (*types.QueryParticipantCollateralResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}
	sdkCtx := sdk.UnwrapSDKContext(ctx)

	collateral, found := k.GetCollateral(sdkCtx, req.Participant)
	if !found {
		return nil, status.Errorf(codes.NotFound, "no collateral found for participant %s", req.Participant)
	}

	return &types.QueryParticipantCollateralResponse{Collateral: collateral}, nil
}

// UnbondingCollateral queries the unbonding collateral of a specific participant.
func (k Keeper) UnbondingCollateral(ctx context.Context, req *types.QueryUnbondingCollateralRequest) (*types.QueryUnbondingCollateralResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}
	sdkCtx := sdk.UnwrapSDKContext(ctx)

	unbondings := k.GetUnbondingByParticipant(sdkCtx, req.Participant)

	return &types.QueryUnbondingCollateralResponse{Unbondings: unbondings}, nil
}
