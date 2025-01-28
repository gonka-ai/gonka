package keeper

import (
	"context"
	"fmt"
	"github.com/productscience/inference/x/inference/types"
)

func (k Keeper) SetUnitOfComputePriceProposal(ctx context.Context, proposal *types.UnitOfComputePriceProposal) {
	SetValue(k, ctx, proposal, types.KeyPrefix(types.UnitOfComputeProposalKeyPrefix), types.UnitOfComputeProposalKey(proposal.Participant))
}

// TODO: fix name!
func (k Keeper) GettUnitOfComputePriceProposal(ctx context.Context, participant string) (*types.UnitOfComputePriceProposal, bool) {
	var object types.UnitOfComputePriceProposal
	return GetValue(k, ctx, &object, types.KeyPrefix(types.UnitOfComputeProposalKeyPrefix), types.UnitOfComputeProposalKey(participant))
}

func (k Keeper) AllUnitOfComputePriceProposals(ctx context.Context) ([]*types.UnitOfComputePriceProposal, error) {
	store := PrefixStore(ctx, k, types.KeyPrefix(types.UnitOfComputeProposalKeyPrefix))
	iterator := store.Iterator(nil, nil)
	defer iterator.Close()

	var proposals []*types.UnitOfComputePriceProposal
	for ; iterator.Valid(); iterator.Next() {
		value := iterator.Value()

		var proposal types.UnitOfComputePriceProposal
		if err := k.cdc.Unmarshal(value, &proposal); err != nil {
			return nil, fmt.Errorf("failed to unmarshal UnitOfComputePriceProposal: %w", err)
		}

		proposals = append(proposals, &proposal)
	}

	return proposals, nil
}

func (k Keeper) GetCurrentUnitOfComputePrice(ctx context.Context) (*uint64, error) {
	epochGroup, err := k.GetCurrentEpochGroup(ctx)
	if err != nil {
		return nil, err
	}

	return &epochGroup.GroupData.UnitOfComputePrice, nil
}
