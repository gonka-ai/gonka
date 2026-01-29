package v0_2_4

import (
	"context"

	"cosmossdk.io/collections"
	upgradetypes "cosmossdk.io/x/upgrade/types"
	"github.com/cosmos/cosmos-sdk/types/module"
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
		err := setPruningDefaults(ctx, k, fromVM)
		if err != nil {
			return fromVM, err
		}

		toVM, err := mm.RunMigrations(ctx, configurator, fromVM)
		if err != nil {
			return toVM, err
		}

		k.Logger().Info("successfully upgraded to " + UpgradeName)
		return toVM, nil
	}
}

func setPruningDefaults(ctx context.Context, k keeper.Keeper, fromVM module.VersionMap) error {
	// We can get away with this at the time we introduce pruning because of lower volume:
	err := k.Inferences.Walk(ctx, nil, func(key string, value types.Inference) (bool, error) {
		pk := collections.Join(int64(value.EpochId), key)
		err := k.InferencesToPrune.Set(ctx, pk, collections.NoValue{})
		return false, err
	})
	if err != nil {
		k.LogError("Failed to set InferencesToPrune", types.Pruning, "error", err)
		return err
	}
	err = k.PruningState.Set(ctx, types.PruningState{
		InferencePrunedEpoch:  0,
		PocBatchesPrunedEpoch: 0,
	})
	if err != nil {
		return err
	}
	params, err := k.GetParams(ctx)
	if err != nil {
		return err
	}
	// Also set invalidations limits here as well
	params.BandwidthLimitsParams.InvalidationsLimit = 500
	params.BandwidthLimitsParams.InvalidationsLimitCurve = 250
	params.BandwidthLimitsParams.InvalidationsSamplePeriod = 120

	params.EpochParams.InferencePruningMax = 5000
	params.EpochParams.PocPruningMax = 1000
	return k.SetParams(ctx, params)
}
