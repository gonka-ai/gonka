package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/query"
	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (k Keeper) SettleAmountAll(ctx context.Context, req *types.QueryAllSettleAmountRequest) (*types.QueryAllSettleAmountResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	settleAmounts, pageRes, err := query.CollectionPaginate(
		ctx,
		k.SettleAmounts,
		req.Pagination,
		func(_ sdk.AccAddress, v types.SettleAmount) (types.SettleAmount, error) { return v, nil },
	)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	return &types.QueryAllSettleAmountResponse{SettleAmount: settleAmounts, Pagination: pageRes}, nil
}

func (k Keeper) SettleAmount(ctx context.Context, req *types.QueryGetSettleAmountRequest) (*types.QueryGetSettleAmountResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	val, found := k.GetSettleAmount(
		ctx,
		req.Participant,
	)
	if !found {
		return nil, status.Error(codes.NotFound, "not found")
	}

	return &types.QueryGetSettleAmountResponse{SettleAmount: val}, nil
}
