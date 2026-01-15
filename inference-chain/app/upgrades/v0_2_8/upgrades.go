package v0_2_8

import (
	"context"
	"fmt"

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

		err := burnExtraCommunityCoins(ctx, &k)
		if err != nil {
			k.LogError("Error removing community account", types.Tokenomics, "err", err)
		}

		toVM, err := mm.RunMigrations(ctx, configurator, fromVM)
		if err != nil {
			return toVM, err
		}

		k.Logger().Info("successfully upgraded to " + UpgradeName)
		return toVM, nil
	}
}

func burnExtraCommunityCoins(ctx context.Context, k *keeper.Keeper) error {
	// This account and it's coins were inadvertently created during genesis. The coins are NOT
	// part of the economic plan for Gonka. The actual community pool coins will not be impacted.
	const moduleName = "pre_programmed_sale"
	expectedAddr := "gonka1rmac644w5hjsyxfggz6e4empxf02vegkt3ppec"

	actualAddr := k.AccountKeeper.GetModuleAddress(moduleName)
	if actualAddr == nil {
		return fmt.Errorf("module account '%s' does not exist", moduleName)
	}

	actualBech32 := actualAddr.String()
	if actualBech32 != expectedAddr {
		return fmt.Errorf("module account address mismatch: expected %s, got %s", expectedAddr, actualBech32)
	}

	coins := k.BankView.SpendableCoins(ctx, actualAddr)
	if coins.IsZero() {
		k.LogInfo("No coins to burn in 'pre_programmed_sale' account", types.Tokenomics, "coins", coins)
		return nil
	}

	err := k.BankKeeper.BurnCoins(ctx, moduleName, coins, "one-time burn of pre_programmed_sale account")
	if err != nil {
		return fmt.Errorf("failed to burn coins: %w", err)
	}

	k.LogInfo("Successfully burned all coins from 'pre_programmed_sale'", types.Tokenomics, "coins", coins)
	return nil
}
