package v1_18

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
		k.LogInfo(fmt.Sprintf("%s - Starting Tokenomics V2 + Dynamic Pricing upgrade", UpgradeName), types.Upgrades)

		for moduleName, version := range vm {
			fmt.Printf("Module: %s, Version: %d\n", moduleName, version)
		}
		fmt.Printf("OrderMigrations: %v\n", mm.OrderMigrations)

		// Get current parameters
		params := k.GetParams(ctx)

		// ========== TOKENOMICS V2 INITIALIZATION ==========

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

		// Initialize Bitcoin reward parameters in the inference module
		k.LogInfo(fmt.Sprintf("%s - Initializing Bitcoin reward parameters", UpgradeName), types.Upgrades)
		params.BitcoinRewardParams.UseBitcoinRewards = true                                 // Enable Bitcoin reward system
		params.BitcoinRewardParams.InitialEpochReward = 285000000000000                     // 285,000 gonka coins per epoch (285,000 * 1,000,000,000 nicoins)
		params.BitcoinRewardParams.DecayRate = types.DecimalFromFloat(-0.000475)            // Exponential decay rate (~4 year halving)
		params.BitcoinRewardParams.GenesisEpoch = 1                                         // Start from epoch 0
		params.BitcoinRewardParams.UtilizationBonusFactor = types.DecimalFromFloat(0.5)     // 50% utilization bonus factor
		params.BitcoinRewardParams.FullCoverageBonusFactor = types.DecimalFromFloat(1.2)    // 20% bonus for full model coverage
		params.BitcoinRewardParams.PartialCoverageBonusFactor = types.DecimalFromFloat(0.1) // 10% bonus scaling for partial coverage

		// ========== DYNAMIC PRICING INITIALIZATION ==========

		// Initialize dynamic pricing parameters with default values (DISABLED initially for safe deployment)
		k.LogInfo(fmt.Sprintf("%s - Initializing dynamic pricing parameters", UpgradeName), types.Upgrades)
		params.DynamicPricingParams.StabilityZoneLowerBound = types.DecimalFromFloat(0.40) // 40% lower bound
		params.DynamicPricingParams.StabilityZoneUpperBound = types.DecimalFromFloat(0.60) // 60% upper bound
		params.DynamicPricingParams.PriceElasticity = types.DecimalFromFloat(0.05)         // 5% elasticity (2% max change per block)
		params.DynamicPricingParams.UtilizationWindowDuration = 60                         // 60 second utilization window
		params.DynamicPricingParams.MinPerTokenPrice = 1                                   // 1 nicoin minimum price floor
		params.DynamicPricingParams.BasePerTokenPrice = 1000                               // 1000 nicoin base price after grace period
		params.DynamicPricingParams.GracePeriodEndEpoch = 90                               // No grace period (start dynamic pricing immediately)
		params.DynamicPricingParams.GracePeriodPerTokenPrice = 0                           // No grace period price

		// Set the updated parameters
		err := k.SetParams(ctx, params)
		if err != nil {
			k.LogError(fmt.Sprintf("%s - Failed to set parameters during upgrade", UpgradeName), types.Upgrades, "error", err)
			return nil, err
		}

		// ========== CAPACITY CACHE INITIALIZATION ==========

		// Initialize capacity cache for all active models from current epoch group data
		k.LogInfo(fmt.Sprintf("%s - Initializing capacity cache for active models", UpgradeName), types.Upgrades)
		err = k.CacheAllModelCapacities(ctx)
		if err != nil {
			// Log error but don't fail the upgrade - capacity cache can be rebuilt
			k.LogError(fmt.Sprintf("%s - Failed to initialize capacity cache", UpgradeName), types.Upgrades, "error", err)
		} else {
			k.LogInfo(fmt.Sprintf("%s - Capacity cache initialized successfully", UpgradeName), types.Upgrades)
		}

		// ========== INITIAL PRICING DATA SETUP ==========

		// Initialize pricing data for all active models at base price
		k.LogInfo(fmt.Sprintf("%s - Initializing pricing data for active models", UpgradeName), types.Upgrades)

		// Get current epoch group to find active models
		currentEpochGroup, err := k.GetCurrentEpochGroup(ctx)
		if err != nil {
			k.LogError(fmt.Sprintf("%s - Failed to get current epoch group for pricing initialization", UpgradeName), types.Upgrades, "error", err)
		} else {
			mainEpochData := currentEpochGroup.GroupData
			if mainEpochData == nil {
				k.LogError(fmt.Sprintf("%s - Epoch group data is nil", UpgradeName), types.Upgrades)
			} else {
				// Initialize pricing for all models in the epoch group
				modelsInitialized := 0
				for _, modelId := range mainEpochData.SubGroupModels {
					err = k.SetModelCurrentPrice(ctx, modelId, params.DynamicPricingParams.BasePerTokenPrice)
					if err != nil {
						k.LogError(fmt.Sprintf("%s - Failed to initialize price for model", UpgradeName), types.Upgrades, "modelId", modelId, "error", err)
					} else {
						modelsInitialized++
					}
				}
				k.LogInfo(fmt.Sprintf("%s - Initialized pricing data", UpgradeName), types.Upgrades,
					"modelsInitialized", modelsInitialized,
					"totalModels", len(mainEpochData.SubGroupModels),
					"basePrice", params.DynamicPricingParams.BasePerTokenPrice)
			}
		}

		// Validate the parameters were set correctly
		updatedParams := k.GetParams(ctx)
		k.LogInfo(fmt.Sprintf("%s - Parameter validation", UpgradeName), types.Upgrades,
			// Collateral parameters
			"BaseWeightRatio", updatedParams.CollateralParams.BaseWeightRatio.String(),
			"CollateralPerWeightUnit", updatedParams.CollateralParams.CollateralPerWeightUnit.String(),
			"SlashFractionInvalid", updatedParams.CollateralParams.SlashFractionInvalid.String(),
			"SlashFractionDowntime", updatedParams.CollateralParams.SlashFractionDowntime.String(),
			"DowntimeMissedPercentageThreshold", updatedParams.CollateralParams.DowntimeMissedPercentageThreshold.String(),
			"CollateralGracePeriodEndEpoch", updatedParams.CollateralParams.GracePeriodEndEpoch,
			// Vesting parameters
			"WorkVestingPeriod", updatedParams.TokenomicsParams.WorkVestingPeriod,
			"RewardVestingPeriod", updatedParams.TokenomicsParams.RewardVestingPeriod,
			"TopMinerVestingPeriod", updatedParams.TokenomicsParams.TopMinerVestingPeriod,
			// Bitcoin reward parameters
			"UseBitcoinRewards", updatedParams.BitcoinRewardParams.UseBitcoinRewards,
			"InitialEpochReward", updatedParams.BitcoinRewardParams.InitialEpochReward,
			"DecayRate", updatedParams.BitcoinRewardParams.DecayRate.String(),
			"GenesisEpoch", updatedParams.BitcoinRewardParams.GenesisEpoch,
			"UtilizationBonusFactor", updatedParams.BitcoinRewardParams.UtilizationBonusFactor.String(),
			"FullCoverageBonusFactor", updatedParams.BitcoinRewardParams.FullCoverageBonusFactor.String(),
			"PartialCoverageBonusFactor", updatedParams.BitcoinRewardParams.PartialCoverageBonusFactor.String(),
			// Dynamic pricing parameters
			"StabilityZoneLowerBound", updatedParams.DynamicPricingParams.StabilityZoneLowerBound.String(),
			"StabilityZoneUpperBound", updatedParams.DynamicPricingParams.StabilityZoneUpperBound.String(),
			"PriceElasticity", updatedParams.DynamicPricingParams.PriceElasticity.String(),
			"UtilizationWindowDuration", updatedParams.DynamicPricingParams.UtilizationWindowDuration,
			"MinPerTokenPrice", updatedParams.DynamicPricingParams.MinPerTokenPrice,
			"BasePerTokenPrice", updatedParams.DynamicPricingParams.BasePerTokenPrice,
			"DynamicPricingGracePeriodEndEpoch", updatedParams.DynamicPricingParams.GracePeriodEndEpoch,
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
		k.LogInfo(fmt.Sprintf("%s - Tokenomics V2 + Dynamic Pricing upgrade completed successfully", UpgradeName), types.Upgrades,
			"collateralModuleAdded", true,
			"streamvestingModuleAdded", true,
			"bitcoinRewardParametersInitialized", true,
			"dynamicPricingParametersInitialized", true,
			"capacityCacheInitialized", true,
			"modelPricingDataInitialized", true,
			"parametersInitialized", true)

		return migratedVm, nil
	}
}
