package v1_15

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
	k keeper.Keeper) upgradetypes.UpgradeHandler {
	return func(ctx context.Context, plan upgradetypes.Plan, vm module.VersionMap) (module.VersionMap, error) {

		// Log the start of the upgrade process
		k.LogInfo(fmt.Sprintf("%s - Starting Tokenomics V2 upgrade", UpgradeName), types.Upgrades)

		for moduleName, version := range vm {
			fmt.Printf("Module: %s, Version: %d\n", moduleName, version)
		}
		fmt.Printf("OrderMigrations: %v\n", mm.OrderMigrations)

		// Get current parameters
		params := k.GetParams(ctx)

		// Initialize collateral parameters in the inference module
		k.LogInfo(fmt.Sprintf("%s - Initializing collateral parameters", UpgradeName), types.Upgrades)
		params.CollateralParams.BaseWeightRatio = types.DecimalFromFloat(0.2)                    // 20%
		params.CollateralParams.CollateralPerWeightUnit = types.DecimalFromFloat(1)              // 1 coin per weight unit
		params.CollateralParams.SlashFractionInvalid = types.DecimalFromFloat(0.20)              // 20% slash for invalid
		params.CollateralParams.SlashFractionDowntime = types.DecimalFromFloat(0.10)             // 10% slash for downtime
		params.CollateralParams.DowntimeMissedPercentageThreshold = types.DecimalFromFloat(0.05) // 5% missed threshold
		params.CollateralParams.GracePeriodEndEpoch = 180                                        // Grace period ends at epoch 180

		// Initialize vesting parameters in the inference module (keeping at 0 as per user change)
		k.LogInfo(fmt.Sprintf("%s - Initializing vesting parameters", UpgradeName), types.Upgrades)
		params.TokenomicsParams.WorkVestingPeriod = 0     // No vesting initially
		params.TokenomicsParams.RewardVestingPeriod = 0   // No vesting initially
		params.TokenomicsParams.TopMinerVestingPeriod = 0 // No vesting initially

		// Set the updated parameters
		err := k.SetParams(ctx, params)
		if err != nil {
			k.LogError(fmt.Sprintf("%s - Failed to set parameters during upgrade", UpgradeName), types.Upgrades, "error", err)
			return nil, err
		}

		// Validate the parameters were set correctly
		updatedParams := k.GetParams(ctx)
		k.LogInfo(fmt.Sprintf("%s - Parameter validation", UpgradeName), types.Upgrades,
			"BaseWeightRatio", updatedParams.CollateralParams.BaseWeightRatio.String(),
			"CollateralPerWeightUnit", updatedParams.CollateralParams.CollateralPerWeightUnit.String(),
			"SlashFractionInvalid", updatedParams.CollateralParams.SlashFractionInvalid.String(),
			"SlashFractionDowntime", updatedParams.CollateralParams.SlashFractionDowntime.String(),
			"DowntimeMissedPercentageThreshold", updatedParams.CollateralParams.DowntimeMissedPercentageThreshold.String(),
			"GracePeriodEndEpoch", updatedParams.CollateralParams.GracePeriodEndEpoch,
			"WorkVestingPeriod", updatedParams.TokenomicsParams.WorkVestingPeriod,
			"RewardVestingPeriod", updatedParams.TokenomicsParams.RewardVestingPeriod,
			"TopMinerVestingPeriod", updatedParams.TokenomicsParams.TopMinerVestingPeriod,
		)

		// Handle capability module version issue (from existing upgrade patterns)
		if _, ok := vm["capability"]; !ok {
			vm["capability"] = mm.Modules["capability"].(module.HasConsensusVersion).ConsensusVersion()
		}

		// Run module migrations (this will initialize the new module stores)
		k.LogInfo(fmt.Sprintf("%s - Running module migrations", UpgradeName), types.Upgrades)
		migratedVm, err := mm.RunMigrations(ctx, configurator, vm)
		if err != nil {
			k.LogError(fmt.Sprintf("%s - Failed to run module migrations", UpgradeName), types.Upgrades, "error", err)
			return nil, err
		}

		// Log successful completion
		k.LogInfo(fmt.Sprintf("%s - Tokenomics V2 upgrade completed successfully", UpgradeName), types.Upgrades,
			"collateralModuleAdded", true,
			"streamvestingModuleAdded", true,
			"parametersInitialized", true)

		return migratedVm, nil
	}
}
