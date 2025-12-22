package keeper

import (
	"context"

	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/productscience/inference/x/inference/types"
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

func (k Keeper) GetParamsSafe(ctx context.Context) (params types.Params, err error) {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	bz := store.Get(types.ParamsKey)
	if bz == nil {
		return params, nil
	}

	err = k.cdc.Unmarshal(bz, &params)
	if err != nil {
		return types.Params{}, err
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

func (k Keeper) GetV1Params(ctx context.Context) (params types.ParamsV1, err error) {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	bz := store.Get(types.ParamsKey)
	if bz == nil {
		return params, nil
	}

	err = k.cdc.Unmarshal(bz, &params)
	if err != nil {
		return types.ParamsV1{}, err
	}
	return params, nil
}

// GetBandwidthLimitsParams returns bandwidth limits parameters
func (k Keeper) GetBandwidthLimitsParams(ctx context.Context) (*types.BandwidthLimitsParams, error) {
	params := k.GetParams(ctx)
	if params.BandwidthLimitsParams == nil {
		// Return default values if not set
		return &types.BandwidthLimitsParams{
			EstimatedLimitsPerBlockKb: 1024, // Default 1MB per block
			KbPerInputToken: &types.Decimal{
				Value:    23, // 0.0023 = 23 × 10^(-4)
				Exponent: -4,
			},
			KbPerOutputToken: &types.Decimal{
				Value:    64, // 0.64 = 64 × 10^(-2)
				Exponent: -2,
			},
			MaxInferencesPerBlock: 1000, // Default 1000 inferences per block chain-wide
		}, nil
	}
	return params.BandwidthLimitsParams, nil
}
