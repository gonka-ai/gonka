package keeper

import (
	"context"
	"github.com/productscience/inference/x/inference/types"
)

func (k Keeper) SetUnitOfComputePriceProposal(ctx context.Context, proposal *types.UnitOfComputePriceProposal) {
	SetValue(k, ctx, proposal, types.KeyPrefix(types.UnitOfComputeProposalKeyPrefix), types.UnitOfComputeProposalKey(proposal.Participant))
}

// TODO: fix name!
func (k Keeper) GettUnitOfComputePriceProposal(ctx context.Context, participant string) (*types.UnitOfComputePriceProposal, bool) {
	var object types.UnitOfComputePriceProposal
	return GetValue(&k, ctx, &object, types.KeyPrefix(types.UnitOfComputeProposalKeyPrefix), types.UnitOfComputeProposalKey(participant))
}

func (k Keeper) AllUnitOfComputePriceProposals(ctx context.Context) ([]*types.UnitOfComputePriceProposal, error) {
	return GetAllValues(ctx, k, types.KeyPrefix(types.UnitOfComputeProposalKeyPrefix), func() *types.UnitOfComputePriceProposal { return &types.UnitOfComputePriceProposal{} })
}

func (k Keeper) GetCurrentUnitOfComputePrice(ctx context.Context) (*uint64, error) {
	epochGroup, err := k.GetCurrentEpochGroup(ctx)
	if err != nil {
		return nil, err
	}

	price := uint64(epochGroup.GroupData.UnitOfComputePrice)
	return &price, nil
}
