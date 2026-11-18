package v0_2_2

import (
	"context"

	"github.com/cosmos/cosmos-sdk/types/module"
	upgradetypes "github.com/cosmos/cosmos-sdk/x/upgrade/types"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
)

func CreateUpgradeHandler(
	mm *module.Manager,
	configurator module.Configurator,
	k keeper.Keeper) upgradetypes.UpgradeHandler {
	return func(ctx context.Context, plan upgradetypes.Plan, fromVM module.VersionMap) (module.VersionMap, error) {

		k.Logger().Info("starting upgrade to " + UpgradeName)
		if _, ok := fromVM["capability"]; !ok {
			fromVM["capability"] = mm.Modules["capability"].(module.HasConsensusVersion).ConsensusVersion()
		}

		toVM, err := mm.RunMigrations(ctx, configurator, fromVM)
		if err != nil {
			return toVM, err
		}

		k.Logger().Info("setting MLNodeVersion in inference module state")

		initialVersion := types.MLNodeVersion{CurrentVersion: "v3.0.8"}
		k.SetMLNodeVersion(ctx, initialVersion)
		k.Logger().Info("successfully set MLNodeVersion")

		return toVM, nil
	}
}
