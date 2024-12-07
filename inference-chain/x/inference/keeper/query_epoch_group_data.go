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

func (k Keeper) EpochGroupDataAll(ctx context.Context, req *types.QueryAllEpochGroupDataRequest) (*types.QueryAllEpochGroupDataResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	var epochGroupDatas []types.EpochGroupData

	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	epochGroupDataStore := prefix.NewStore(store, types.KeyPrefix(types.EpochGroupDataKeyPrefix))

	pageRes, err := query.Paginate(epochGroupDataStore, req.Pagination, func(key []byte, value []byte) error {
		var epochGroupData types.EpochGroupData
		if err := k.cdc.Unmarshal(value, &epochGroupData); err != nil {
			return err
		}

		epochGroupDatas = append(epochGroupDatas, epochGroupData)
		return nil
	})

	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	return &types.QueryAllEpochGroupDataResponse{EpochGroupData: epochGroupDatas, Pagination: pageRes}, nil
}

func (k Keeper) EpochGroupData(ctx context.Context, req *types.QueryGetEpochGroupDataRequest) (*types.QueryGetEpochGroupDataResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	val, found := k.GetEpochGroupData(
		ctx,
		req.PocStartBlockHeight,
	)
	if !found {
		return nil, status.Error(codes.NotFound, "not found")
	}

	return &types.QueryGetEpochGroupDataResponse{EpochGroupData: val}, nil
}
