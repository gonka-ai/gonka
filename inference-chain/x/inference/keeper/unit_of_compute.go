package keeper

import (
	"context"

	"github.com/productscience/inference/x/inference/types"
)

func (k Keeper) SetUnitOfComputePriceProposal(ctx context.Context, proposal *types.UnitOfComputePriceProposal) {
	if err := k.UnitOfComputePriceProposals.Set(ctx, proposal.Participant, *proposal); err != nil {
		panic(err)
	}
}

// TODO: fix name!
func (k Keeper) GettUnitOfComputePriceProposal(ctx context.Context, participant string) (*types.UnitOfComputePriceProposal, bool) {
	v, err := k.UnitOfComputePriceProposals.Get(ctx, participant)
	if err != nil {
		return nil, false
	}
	return &v, true
}

func (k Keeper) AllUnitOfComputePriceProposals(ctx context.Context) ([]*types.UnitOfComputePriceProposal, error) {
	iter, err := k.UnitOfComputePriceProposals.Iterate(ctx, nil)
	if err != nil {
		return nil, err
	}
	values, err := iter.Values()
	if err != nil {
		return nil, err
	}
	out := make([]*types.UnitOfComputePriceProposal, 0, len(values))
	for i := range values {
		v := values[i]
		out = append(out, &v)
	}
	return out, nil
}

func (k Keeper) GetCurrentUnitOfComputePrice(ctx context.Context) (*uint64, error) {
	epochGroup, err := k.GetCurrentEpochGroup(ctx)
	if err != nil {
		return nil, err
	}

	price := uint64(epochGroup.GroupData.UnitOfComputePrice)
	return &price, nil
}
