package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/query"
	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (k Keeper) TopMinerAll(ctx context.Context, req *types.QueryAllTopMinerRequest) (*types.QueryAllTopMinerResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	_ = sdk.UnwrapSDKContext(ctx)

	items, pageRes, err := query.CollectionPaginate(
		ctx,
		k.TopMiners,
		req.Pagination,
		func(_ sdk.AccAddress, v types.TopMiner) (types.TopMiner, error) { return v, nil },
	)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	return &types.QueryAllTopMinerResponse{TopMiner: items, Pagination: pageRes}, nil
}

func (k Keeper) TopMiner(ctx context.Context, req *types.QueryGetTopMinerRequest) (*types.QueryGetTopMinerResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	val, found := k.GetTopMiner(
		ctx,
		req.Address,
	)
	if !found {
		return nil, status.Error(codes.NotFound, "not found")
	}

	return &types.QueryGetTopMinerResponse{TopMiner: val}, nil
}
