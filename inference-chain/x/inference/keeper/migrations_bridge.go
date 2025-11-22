package keeper

import (
	"github.com/cosmos/cosmos-sdk/runtime"
	sdk "github.com/cosmos/cosmos-sdk/types"
)

const (
	legacyTokenCodeIDKey     = "TokenCodeID"
	legacyContractsParamsKey = "contracts_params"
)

// MigrateLegacyBridgeState upgrades bridge-related state from versions prior to v0.2.5.
func (k Keeper) MigrateLegacyBridgeState(ctx sdk.Context) error {
	k.removeLegacyCosmWasmArtifacts(ctx)
	return nil
}

func (k Keeper) removeLegacyCosmWasmArtifacts(ctx sdk.Context) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := storeAdapter

	store.Delete([]byte(legacyTokenCodeIDKey))
	store.Delete([]byte(legacyContractsParamsKey))
}
