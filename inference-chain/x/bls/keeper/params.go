package keeper

import (
	"context"
	"fmt"

	"github.com/cosmos/cosmos-sdk/runtime"

	"github.com/productscience/inference/x/bls/types"
)

// GetParams get all parameters as types.Params
func (k Keeper) GetParams(ctx context.Context) (params types.Params, err error) {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	bz := store.Get(types.ParamsKey)
	if bz == nil {
		return params, nil
	}

	err = k.cdc.Unmarshal(bz, &params)
	return params, err
}

// SetParams set the params after validating cross-parameter invariants.
// Without validation, a governance proposal could set TSlotsDegreeOffset >= ITotalSlots,
// causing the threshold calculation to underflow and making BLS signing impossible (DoS)
// or reducing the threshold to near-zero (security bypass).
func (k Keeper) SetParams(ctx context.Context, params types.Params) error {
	if err := params.Validate(); err != nil {
		return fmt.Errorf("invalid BLS params: %w", err)
	}
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
	params, err := k.GetParams(ctx)
	if err != nil {
		return 0
	}
	return params.ITotalSlots
}

// GetTSlotsDegreeOffset returns the polynomial degree offset
func (k Keeper) GetTSlotsDegreeOffset(ctx context.Context) uint32 {
	params, err := k.GetParams(ctx)
	if err != nil {
		return 0
	}
	return params.TSlotsDegreeOffset
}

// GetDealingPhaseDurationBlocks returns the dealing phase duration in blocks
func (k Keeper) GetDealingPhaseDurationBlocks(ctx context.Context) int64 {
	params, err := k.GetParams(ctx)
	if err != nil {
		return 0
	}
	return params.DealingPhaseDurationBlocks
}

// GetVerificationPhaseDurationBlocks returns the verification phase duration in blocks
func (k Keeper) GetVerificationPhaseDurationBlocks(ctx context.Context) int64 {
	params, err := k.GetParams(ctx)
	if err != nil {
		return 0
	}
	return params.VerificationPhaseDurationBlocks
}
