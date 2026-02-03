package v0_2_10

import (
	"context"

	upgradetypes "cosmossdk.io/x/upgrade/types"
	"github.com/cosmos/cosmos-sdk/types/module"
	"github.com/productscience/inference/x/inference/keeper"
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

		toVM, err := mm.RunMigrations(ctx, configurator, fromVM)
		if err != nil {
			return toVM, err
		}

		k.Logger().Info("successfully upgraded to " + UpgradeName)
		return toVM, nil
	}
}
