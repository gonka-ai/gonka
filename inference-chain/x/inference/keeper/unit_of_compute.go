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
	return GetValue(k, ctx, &object, types.KeyPrefix(types.UnitOfComputeProposalKeyPrefix), types.UnitOfComputeProposalKey(participant))
}

func (k Keeper) AllUnitOfComputePriceProposals(ctx context.Context) (proposals []*types.UnitOfComputePriceProposal) {
	store := PrefixStore(ctx, k, types.KeyPrefix(types.UnitOfComputeProposalKeyPrefix))
	iterator := store.Iterator(nil, nil)
	defer iterator.Close()

	for ; iterator.Valid(); iterator.Next() {
		// TODO: parse values!
	}
}

func (k Keeper) SetUnitOfComputePrice(ctx context.Context, price uint64, epochId uint64) {
	object := &types.UnitOfComputePrice{
		Price:   price,
		EpochId: epochId,
	}
	SetValue(k, ctx, object, types.KeyPrefix(types.UnitOfComputePriceKeyPrefix), types.UnitOfComputePriceKey(epochId))
}

/*func (k Keeper) GetUnitOfComputePriceProposal(ctx context.Context, participant string) (val types.UnitOfComputePriceProposal, found bool) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, types.KeyPrefix(types.SettleAmountKeyPrefix))
	b := store.Get(types.SettleAmountKey(participant))
	if b == nil {
		return val, false
	}
	k.cdc.MustUnmarshal(b, &val)
	return val, true
}*/
