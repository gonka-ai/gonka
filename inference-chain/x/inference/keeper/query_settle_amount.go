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

func (k Keeper) SettleAmountAll(ctx context.Context, req *types.QueryAllSettleAmountRequest) (*types.QueryAllSettleAmountResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	var settleAmounts []types.SettleAmount

	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	settleAmountStore := prefix.NewStore(store, types.KeyPrefix(types.SettleAmountKeyPrefix))

	pageRes, err := query.Paginate(settleAmountStore, req.Pagination, func(key []byte, value []byte) error {
		var settleAmount types.SettleAmount
		if err := k.cdc.Unmarshal(value, &settleAmount); err != nil {
			return err
		}

		settleAmounts = append(settleAmounts, settleAmount)
		return nil
	})

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
