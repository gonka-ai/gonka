package v0_2_18

import storetypes "github.com/cosmos/cosmos-sdk/store/v2/types"

// Store keys removed with the v0.53 modules this binary no longer mounts.
// The loader drops them before the upgrade handler runs. None of these keys
// are read by the handler or by the ibc-go v11 migrations.
const (
	storeKeyCapability = "capability"
	storeKeyCircuit    = "circuit"
	storeKeyCrisis     = "crisis"
	storeKeyNFT        = "nft"
	storeKeyParams     = "params"
)

// RemovedModuleStores are the sub-stores deleted at the v0.2.18 upgrade height.
func RemovedModuleStores() *storetypes.StoreUpgrades {
	return &storetypes.StoreUpgrades{
		Deleted: []string{
			storeKeyCapability,
			storeKeyCircuit,
			storeKeyCrisis,
			feeIBCModuleName,
			storeKeyNFT,
			storeKeyParams,
		},
	}
}
