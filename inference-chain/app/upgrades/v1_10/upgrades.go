package v1_10

import (
	"context"

	upgradetypes "cosmossdk.io/x/upgrade/types"
	"github.com/cosmos/cosmos-sdk/types/module"
	"github.com/productscience/inference/x/inference/keeper"
)

func CreateUpgradeHandler(
	k keeper.Keeper) upgradetypes.UpgradeHandler {
	return func(ctx context.Context, plan upgradetypes.Plan, vm module.VersionMap) (module.VersionMap, error) {
		// No longer seed CW20 wasm bytes from binary in this upgrade handler.
		// Governance will store code and set code IDs post-genesis.

		return vm, nil
	}
}
