package v0_2_5

import (
	"context"

	upgradetypes "cosmossdk.io/x/upgrade/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/module"
	blskeeper "github.com/productscience/inference/x/bls/keeper"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
)

// CreateUpgradeHandler defines the v0.2.5 upgrade handler.
// Intentionally left with a blank business implementation for now.
// It only performs the standard capability version fix and runs module migrations.
func CreateUpgradeHandler(
	mm *module.Manager,
	configurator module.Configurator,
	k keeper.Keeper,
	blsKeeper blskeeper.Keeper,
) upgradetypes.UpgradeHandler {
	return func(ctx context.Context, plan upgradetypes.Plan, fromVM module.VersionMap) (module.VersionMap, error) {
		k.Logger().Info("starting upgrade to " + UpgradeName)

		// Ensure capability module has a version to avoid InitGenesis panic.
		if _, ok := fromVM["capability"]; !ok {
			fromVM["capability"] = mm.Modules["capability"].(module.HasConsensusVersion).ConsensusVersion()
		}

		err := setNewInvalidationParams(ctx, k, fromVM)
		if err != nil {
			return nil, err
		}

		// Ensure bridge escrow account exists
		sdkCtx := sdk.UnwrapSDKContext(ctx)
		k.AccountKeeper.GetModuleAccount(sdkCtx, types.BridgeEscrowAccName)
		k.Logger().Info("v0.2.5 upgrade: ensured bridge_escrow module account exists")

		if cleared := k.ClearWrappedTokenCodeID(sdkCtx); cleared {
			k.Logger().Info("v0.2.5 upgrade: cleared wrapped token code ID from state")
		}

		// Run default module migrations (includes confirmation weight initialization).
		toVM, err := mm.RunMigrations(ctx, configurator, fromVM)
		if err != nil {
			return toVM, err
		}

		k.Logger().Info("successfully upgraded to " + UpgradeName)
		return toVM, nil
	}
}

func setNewInvalidationParams(ctx context.Context, k keeper.Keeper, vm module.VersionMap) error {
	params, err := k.GetParamsSafe(ctx)
	if err != nil {
		return err
	}
	params.ValidationParams.InvalidReputationPreserve = types.DecimalFromFloat(0.0)
	params.ValidationParams.BadParticipantInvalidationRate = types.DecimalFromFloat(0.1)
	params.ValidationParams.InvalidationHThreshold = types.DecimalFromFloat(4.0)
	// For now, effectively disable the downtime activity. 32k consecutive failures would be required
	params.ValidationParams.DowntimeGoodPercentage = types.DecimalFromFloat(0.98)
	params.ValidationParams.DowntimeBadPercentage = types.DecimalFromFloat(0.99)
	params.ValidationParams.DowntimeHThreshold = types.DecimalFromFloat(100.0)
	params.ValidationParams.DowntimeReputationPreserve = types.DecimalFromFloat(0.0)
	params.ValidationParams.QuickFailureThreshold = types.DecimalFromFloat(0.000001)
	params.BandwidthLimitsParams.MinimumConcurrentInvalidations = 1
	if params.ConfirmationPocParams == nil {
		params.ConfirmationPocParams = &types.ConfirmationPoCParams{}
	}
	params.ConfirmationPocParams.ExpectedConfirmationsPerEpoch = 1
	params.ConfirmationPocParams.AlphaThreshold = types.DecimalFromFloat(0.5)
	params.ConfirmationPocParams.SlashFraction = types.DecimalFromFloat(0.0)
	params.ConfirmationPocParams.UpgradeProtectionWindow = 500
	params.EpochParams.PocSlotAllocation = types.DecimalFromFloat(0.1)
	return k.SetParams(ctx, params)
}
