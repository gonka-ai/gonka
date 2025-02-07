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

func (k Keeper) TopMinerAll(ctx context.Context, req *types.QueryAllTopMinerRequest) (*types.QueryAllTopMinerResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	var topMiners []types.TopMiner

	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	topMinerStore := prefix.NewStore(store, types.KeyPrefix(types.TopMinerKeyPrefix))

	pageRes, err := query.Paginate(topMinerStore, req.Pagination, func(key []byte, value []byte) error {
		var topMiner types.TopMiner
		if err := k.cdc.Unmarshal(value, &topMiner); err != nil {
			return err
		}

		topMiners = append(topMiners, topMiner)
		return nil
	})

	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	return &types.QueryAllTopMinerResponse{TopMiner: topMiners, Pagination: pageRes}, nil
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
