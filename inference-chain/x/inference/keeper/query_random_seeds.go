package keeper

import (
	"context"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ListRandomSeeds returns all random seeds for a given epoch index.
func (k Keeper) ListRandomSeeds(ctx context.Context, req *types.QueryRandomSeedsRequest) (*types.QueryRandomSeedsResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	var seeds []*types.RandomSeed
	it, err := k.RandomSeeds.Iterate(ctx, collections.NewPrefixedPairRange[uint64, sdk.AccAddress](req.EpochIndex))
	if err != nil {
		return nil, status.Error(codes.Internal, "failed to iterate random seeds")
	}
	defer it.Close()

	for ; it.Valid(); it.Next() {
		kv, err := it.KeyValue()
		if err != nil {
			return nil, status.Error(codes.Internal, "failed to read random seed value")
		}
		seed := restoredRandomSeed(kv.Key, kv.Value)
		seeds = append(seeds, &seed)
	}

	return &types.QueryRandomSeedsResponse{Seeds: seeds}, nil
}
