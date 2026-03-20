package keeper

import (
	"context"

	"github.com/cosmos/cosmos-sdk/types/query"
	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (k Keeper) EpochGroupDataAll(ctx context.Context, req *types.QueryAllEpochGroupDataRequest) (*types.QueryAllEpochGroupDataResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	all := k.GetAllEpochGroupData(ctx)
	pageRes, start, end := paginateSlice(len(all), req.Pagination)
	return &types.QueryAllEpochGroupDataResponse{EpochGroupData: all[start:end], Pagination: pageRes}, nil
}

// paginateSlice computes a window [start, end) over a slice of the given length,
// honouring the Offset / Limit / CountTotal fields of a PageRequest.
func paginateSlice(total int, pg *query.PageRequest) (*query.PageResponse, int, int) {
	if pg == nil {
		pg = &query.PageRequest{}
	}
	offset := int(pg.Offset)
	if offset > total {
		offset = total
	}
	limit := int(pg.Limit)
	if limit == 0 {
		limit = total
	}
	end := offset + limit
	if end > total {
		end = total
	}
	resp := &query.PageResponse{}
	if pg.CountTotal {
		resp.Total = uint64(total)
	}
	if end < total {
		resp.NextKey = []byte{1}
	}
	return resp, offset, end
}

func (k Keeper) EpochGroupData(ctx context.Context, req *types.QueryGetEpochGroupDataRequest) (*types.QueryGetEpochGroupDataResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	val, found := k.GetEpochGroupData(
		ctx,
		req.EpochIndex,
		req.ModelId,
	)
	if !found {
		return nil, status.Error(codes.NotFound, "not found")
	}

	return &types.QueryGetEpochGroupDataResponse{EpochGroupData: val}, nil
}
