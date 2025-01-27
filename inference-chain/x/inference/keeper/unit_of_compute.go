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

func (k Keeper) SetUnitOfComputePrice(ctx context.Context, price uint64, epochId uint64) {
	object := &types.UnitOfComputePrice{
		Price:   price,
		EpochId: epochId,
	}
	SetValue(k, ctx, object, types.KeyPrefix(types.UnitOfComputePriceKeyPrefix), types.UnitOfComputePriceKey(epochId))
}

func (k Keeper) GetUnitOfComputePrice(ctx context.Context, epochId uint64) (*types.UnitOfComputePrice, bool) {
	var object types.UnitOfComputePrice
	return GetValue(k, ctx, &object, types.KeyPrefix(types.UnitOfComputePriceKeyPrefix), types.UnitOfComputePriceKey(epochId))
}

func (k Keeper) GetCurrentUnitOfComputePrice(ctx context.Context) (*types.UnitOfComputePrice, error) {
	epochGroup, err := k.GetCurrentEpochGroup(ctx)
	if err != nil {
		return nil, err
	}

	price, found := k.GetUnitOfComputePrice(ctx, epochGroup.GroupData.EpochGroupId)
	if !found {
		return nil, fmt.Errorf("price not found for epoch %d", epochGroup.GroupData.EpochGroupId)
	}

	return price, nil
}
