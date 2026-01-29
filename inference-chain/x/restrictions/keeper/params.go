package keeper

import (
	"context"

	"github.com/cosmos/cosmos-sdk/runtime"

	"github.com/productscience/inference/x/restrictions/types"
)

// GetParams get all parameters as types.Params
func (k Keeper) GetParams(ctx context.Context) (params types.Params, err error) {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	bz := store.Get(types.ParamsKey)
	if bz == nil {
		return types.DefaultParams(), nil
	}

	err = k.cdc.Unmarshal(bz, &params)
	if err != nil {
		return params, err
	}

	// Ensure slices are never nil (empty instead)
	if params.EmergencyTransferExemptions == nil {
		params.EmergencyTransferExemptions = []types.EmergencyTransferExemption{}
	}
	if params.ExemptionUsageTracking == nil {
		params.ExemptionUsageTracking = []types.ExemptionUsage{}
	}
	return params, nil
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
