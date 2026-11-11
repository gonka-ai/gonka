package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/productscience/inference/x/bls/types"
)

// BeaconKey returns the block beacon key state and activation (rule R9/R6).
func (k Keeper) BeaconKey(goCtx context.Context, req *types.QueryBeaconKeyRequest) (*types.QueryBeaconKeyResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}
	ctx := sdk.UnwrapSDKContext(goCtx)
	resp := &types.QueryBeaconKeyResponse{}
	resp.KeyEpoch, resp.KeySet = k.GetBeaconKeyEpoch(ctx)
	resp.Candidate, resp.HasCandidate = k.GetBeaconKeyCandidate(ctx)
	resp.Activation, resp.Activated = k.GetBeaconActivation(ctx)
	return resp, nil
}

// PendingBeacon returns the message validators sign for a round.
func (k Keeper) PendingBeacon(goCtx context.Context, req *types.QueryPendingBeaconRequest) (*types.QueryPendingBeaconResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}
	ctx := sdk.UnwrapSDKContext(goCtx)
	p, ok := k.GetPendingBeacon(ctx, req.Round)
	if !ok {
		return nil, status.Errorf(codes.NotFound, "no pending beacon for round %d", req.Round)
	}
	return &types.QueryPendingBeaconResponse{Pending: p}, nil
}

// BlockBeacon returns the stored beacon record of a round.
func (k Keeper) BlockBeacon(goCtx context.Context, req *types.QueryBlockBeaconRequest) (*types.QueryBlockBeaconResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}
	r, ok := k.GetBlockBeacon(sdk.UnwrapSDKContext(goCtx), req.Round)
	if !ok {
		return nil, status.Errorf(codes.NotFound, "no beacon record for round %d", req.Round)
	}
	return &types.QueryBlockBeaconResponse{Record: r}, nil
}

// LatestBlockBeacon returns the newest OK beacon record.
func (k Keeper) LatestBlockBeacon(goCtx context.Context, req *types.QueryLatestBlockBeaconRequest) (*types.QueryBlockBeaconResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}
	r, ok := k.GetLatestBlockBeacon(sdk.UnwrapSDKContext(goCtx))
	if !ok {
		return nil, status.Error(codes.NotFound, "no OK beacon record yet")
	}
	return &types.QueryBlockBeaconResponse{Record: r}, nil
}
