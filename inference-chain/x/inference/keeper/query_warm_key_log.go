package keeper

import (
	"context"
	"errors"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/productscience/inference/x/inference/types"
)

func (k Keeper) WarmKeyEntry(ctx context.Context, req *types.QueryWarmKeyEntryRequest) (*types.QueryWarmKeyEntryResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}
	granter, err := sdk.AccAddressFromBech32(req.Granter)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid granter address")
	}
	if req.Id == 0 {
		return nil, status.Error(codes.InvalidArgument, "id must be positive")
	}
	entry, err := k.WarmKeyLogEntriesMap.Get(ctx, collections.Join(granter, req.Id))
	if err != nil {
		if errors.Is(err, collections.ErrNotFound) {
			return nil, status.Error(codes.NotFound, "warm-key entry not found")
		}
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &types.QueryWarmKeyEntryResponse{Entry: &entry}, nil
}

func (k Keeper) WarmKeyLog(ctx context.Context, req *types.QueryWarmKeyLogRequest) (*types.QueryWarmKeyLogResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}
	granter, err := sdk.AccAddressFromBech32(req.Granter)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid granter address")
	}
	entries, err := k.WarmKeyLogEntries(ctx, granter)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	out := make([]*types.WarmKeyLogEntry, len(entries))
	for i := range entries {
		out[i] = &entries[i]
	}
	return &types.QueryWarmKeyLogResponse{Entries: out}, nil
}
