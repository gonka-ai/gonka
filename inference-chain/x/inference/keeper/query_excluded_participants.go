package keeper

import (
	"context"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

// ExcludedParticipantsMap returns the list of excluded participants for the given epoch.
// If req.EpochIndex == 0, it defaults to the current effective epoch.
// Upgrade-safety: read-only query over a new epoch-scoped collection; deterministic ordering.
func (k Keeper) ExcludedParticipants(ctx context.Context, req *types.QueryExcludedParticipantsRequest) (*types.QueryExcludedParticipantsResponse, error) {
	if req == nil {
		return &types.QueryExcludedParticipantsResponse{Items: nil}, nil
	}

	epochIndex := req.EpochIndex
	if epochIndex == 0 {
		if idx, ok := k.GetEffectiveEpochIndex(ctx); ok {
			epochIndex = idx
		} else {
			// No effective epoch yet, return empty list
			return &types.QueryExcludedParticipantsResponse{Items: nil}, nil
		}
	}

	var items []*types.ExcludedParticipant
	it, err := k.ExcludedParticipantsMap.Iterate(ctx, collections.NewPrefixedPairRange[uint64, sdk.AccAddress](epochIndex))
	if err != nil {
		return nil, err
	}
	defer it.Close()
	for ; it.Valid(); it.Next() {
		val, err := it.Value()
		if err != nil {
			return nil, err
		}
		v := val
		items = append(items, &v)
	}

	return &types.QueryExcludedParticipantsResponse{Items: items}, nil
}
