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

// GetGenesisGuardianNetworkMaturityThreshold returns the genesis guardian network maturity threshold from GenesisOnlyParams
func (k Keeper) GetGenesisGuardianNetworkMaturityThreshold(ctx context.Context) int64 {
	params, found := k.GetGenesisOnlyParams(ctx)
	if !found {
		// Return default value if not found
		return 10_000_000
	}
	return params.GenesisGuardianNetworkMaturityThreshold
}

// GetGenesisGuardianMultiplier returns the genesis guardian multiplier from GenesisOnlyParams
func (k Keeper) GetGenesisGuardianMultiplier(ctx context.Context) *types.Decimal {
	params, found := k.GetGenesisOnlyParams(ctx)
	if !found {
		// Return default value if not found
		return types.DecimalFromFloat(0.52)
	}
	return params.GenesisGuardianMultiplier
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

// IsNetworkMature checks if the total network power exceeds the genesis guardian maturity threshold
func (k Keeper) IsNetworkMature(ctx context.Context, totalNetworkPower int64) bool {
	threshold := k.GetGenesisGuardianNetworkMaturityThreshold(ctx)
	return totalNetworkPower >= threshold
}

// GetGenesisGuardianAddresses returns the genesis guardian addresses from GenesisOnlyParams
func (k Keeper) GetGenesisGuardianAddresses(ctx context.Context) []string {
	params, found := k.GetGenesisOnlyParams(ctx)
	if !found {
		// Return empty slice if not found (same as default)
		return []string{}
	}
	return params.GenesisGuardianAddresses
}

// GetGenesisGuardianEnabled returns whether genesis guardian system is enabled from GenesisOnlyParams
func (k Keeper) GetGenesisGuardianEnabled(ctx context.Context) bool {
	params, found := k.GetGenesisOnlyParams(ctx)
	if !found {
		// Return default value if not found (false)
		return false
	}
	return params.GenesisGuardianEnabled
}
