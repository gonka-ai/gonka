package v0_2_10

import (
	"context"

	upgradetypes "cosmossdk.io/x/upgrade/types"
	"github.com/cosmos/cosmos-sdk/types/module"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
)

func CreateUpgradeHandler(
	mm *module.Manager,
	configurator module.Configurator,
	k keeper.Keeper,
) upgradetypes.UpgradeHandler {
	return func(ctx context.Context, plan upgradetypes.Plan, fromVM module.VersionMap) (module.VersionMap, error) {
		k.Logger().Info("starting upgrade to " + UpgradeName)

		if _, ok := fromVM["capability"]; !ok {
			fromVM["capability"] = mm.Modules["capability"].(module.HasConsensusVersion).ConsensusVersion()
		}

		setValidationSlots(ctx, k)

		toVM, err := mm.RunMigrations(ctx, configurator, fromVM)
		if err != nil {
			return toVM, err
		}

		k.Logger().Info("successfully upgraded to " + UpgradeName)
		return toVM, nil
	}
}

// setValidationSlots explicitly sets ValidationSlots to 0 (disabled).
// This keeps O(N^2) validation behavior until sampling is enabled via governance.
// Must be enabled only when new participant cost > 0 (see proposals/poc/optimize.md).
func setValidationSlots(ctx context.Context, k keeper.Keeper) {
	params, err := k.GetParams(ctx)
	if err != nil {
		k.LogError("failed to get params during upgrade", types.Upgrades, "error", err)
		return
	}

	if params.PocParams == nil {
		k.LogError("poc params not initialized", types.Upgrades)
		return
	}

	params.PocParams.ValidationSlots = 0

	if err := k.SetParams(ctx, params); err != nil {
		k.LogError("failed to set params with validation slots", types.Upgrades, "error", err)
		return
	}

	k.LogInfo("set validation slots", types.Upgrades, "validation_slots", params.PocParams.ValidationSlots)
}
