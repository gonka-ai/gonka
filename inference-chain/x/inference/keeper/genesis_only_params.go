package keeper

import (
	"context"

	"cosmossdk.io/store/prefix"
	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/productscience/inference/x/inference/types"
)

func (k Keeper) SetGenesisOnlyParams(ctx context.Context, params *types.GenesisOnlyParams) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, types.KeyPrefix(types.GenesisOnlyDataKey))
	b := k.cdc.MustMarshal(params)
	store.Set([]byte{0}, b)
}

func (k Keeper) GetGenesisOnlyParams(ctx context.Context) (val types.GenesisOnlyParams, found bool) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, types.KeyPrefix(types.GenesisOnlyDataKey))

	b := store.Get([]byte{0})
	if b == nil {
		return val, false
	}

	k.cdc.MustUnmarshal(b, &val)
	return val, true
}

// GetNetworkMaturityThreshold returns the network maturity threshold from GenesisOnlyParams
func (k Keeper) GetNetworkMaturityThreshold(ctx context.Context) int64 {
	params, found := k.GetGenesisOnlyParams(ctx)
	if !found {
		// Return default value if not found
		return 10_000_000
	}
	return params.NetworkMaturityThreshold
}

// GetGenesisVetoMultiplier returns the genesis veto multiplier from GenesisOnlyParams
func (k Keeper) GetGenesisVetoMultiplier(ctx context.Context) *types.Decimal {
	params, found := k.GetGenesisOnlyParams(ctx)
	if !found {
		// Return default value if not found
		return types.DecimalFromFloat(0.52)
	}
	return params.GenesisVetoMultiplier
}

// GetMaxIndividualPowerPercentage returns the max individual power percentage from GenesisOnlyParams
func (k Keeper) GetMaxIndividualPowerPercentage(ctx context.Context) *types.Decimal {
	params, found := k.GetGenesisOnlyParams(ctx)
	if !found {
		// Return nil if not found - this disables power capping
		return nil
	}
	return params.MaxIndividualPowerPercentage
}

// IsNetworkMature checks if the total network power exceeds the maturity threshold
func (k Keeper) IsNetworkMature(ctx context.Context, totalNetworkPower int64) bool {
	threshold := k.GetNetworkMaturityThreshold(ctx)
	return totalNetworkPower >= threshold
}

// GetFirstGenesisValidatorAddress returns the first genesis validator address from GenesisOnlyParams
func (k Keeper) GetFirstGenesisValidatorAddress(ctx context.Context) string {
	params, found := k.GetGenesisOnlyParams(ctx)
	if !found {
		// Return empty string if not found (same as default)
		return ""
	}
	return params.FirstGenesisValidatorAddress
}

// GetGenesisVetoEnabled returns whether genesis veto is enabled from GenesisOnlyParams
func (k Keeper) GetGenesisVetoEnabled(ctx context.Context) bool {
	params, found := k.GetGenesisOnlyParams(ctx)
	if !found {
		// Return default value if not found (false)
		return false
	}
	return params.GenesisVetoEnabled
}
