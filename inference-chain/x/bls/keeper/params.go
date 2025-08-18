package keeper

import (
	"context"

	"github.com/cosmos/cosmos-sdk/runtime"

	"github.com/productscience/inference/x/bls/types"
)

// GetParams get all parameters as types.Params
func (k Keeper) GetParams(ctx context.Context) (params types.Params) {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	bz := store.Get(types.ParamsKey)
	if bz == nil {
		return params
	}

	k.cdc.MustUnmarshal(bz, &params)
	return params
}

// SetParams set the params
func (k Keeper) SetParams(ctx context.Context, params types.Params) error {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	bz, err := k.cdc.Marshal(&params)
	if err != nil {
		return err
	}
	store.Set(types.ParamsKey, bz)

	return nil
}

// Convenient getter methods for individual parameters

// GetITotalSlots returns the total number of slots for DKG
func (k Keeper) GetITotalSlots(ctx context.Context) uint32 {
	return k.GetParams(ctx).ITotalSlots
}

// GetTSlotsDegreeOffset returns the polynomial degree offset
func (k Keeper) GetTSlotsDegreeOffset(ctx context.Context) uint32 {
	return k.GetParams(ctx).TSlotsDegreeOffset
}

// GetDealingPhaseDurationBlocks returns the dealing phase duration in blocks
func (k Keeper) GetDealingPhaseDurationBlocks(ctx context.Context) int64 {
	return k.GetParams(ctx).DealingPhaseDurationBlocks
}

// GetVerificationPhaseDurationBlocks returns the verification phase duration in blocks
func (k Keeper) GetVerificationPhaseDurationBlocks(ctx context.Context) int64 {
	return k.GetParams(ctx).VerificationPhaseDurationBlocks
}
