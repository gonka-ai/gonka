// Package v0_2_18 holds the v0.2.18 upgrade handler.
//
// The capability module is not registered in this binary. Do not read it from
// the module manager: a production version map that omits the entry must not
// panic. RunMigrations only visits modules this binary still has.
//
// If later work bumps a module ConsensusVersion, it must also register the
// corresponding migration in app/upgrades.go's registerMigrations().
package v0_2_18

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/module"
	upgradetypes "github.com/cosmos/cosmos-sdk/x/upgrade/types"

	"github.com/productscience/inference/x/inference/types"
)

func CreateUpgradeHandler(
	mm *module.Manager,
	configurator module.Configurator,
	logger upgradeLogger,
	channelKeeper ChannelVersionKeeper,
	bank BalanceKeeper,
	distr CommunityPoolFunder,
) upgradetypes.UpgradeHandler {
	return func(ctx context.Context, _ upgradetypes.Plan, fromVM module.VersionMap) (module.VersionMap, error) {
		logger.LogInfo("starting upgrade", types.Upgrades, "version", UpgradeName)

		sdkCtx := sdk.UnwrapSDKContext(ctx)
		if err := unwrapICS29ChannelVersions(sdkCtx, channelKeeper, logger); err != nil {
			return fromVM, err
		}
		if err := sweepFeeIBCBalance(ctx, bank, distr, logger); err != nil {
			return fromVM, err
		}

		toVM, err := mm.RunMigrations(ctx, configurator, fromVM)
		if err != nil {
			return toVM, err
		}

		logger.LogInfo("successfully upgraded", types.Upgrades, "version", UpgradeName)
		return toVM, nil
	}
}
