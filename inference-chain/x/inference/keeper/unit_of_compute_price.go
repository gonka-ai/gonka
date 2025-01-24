package keeper

import (
	"context"
	"github.com/productscience/inference/x/inference/types"
)

func (k Keeper) SetUnitOfComputePriceProposal(ctx context.Context, proposal *types.UnitOfComputePriceProposal) {
	SetValue(k, ctx, proposal, types.KeyPrefix(types.UnitOfComputeProposalKeyPrefix), types.UnitOfComputeProposalKey(proposal.Participant))
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
