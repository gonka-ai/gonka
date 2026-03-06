package v0_2_11

import (
	"context"
	"errors"

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
		k.LogInfo("starting upgrade", types.Upgrades, "version", UpgradeName)

		err := setSafetyWindow(ctx, k)
		if err != nil {
			return nil, err
		}
		currentEpochIndex, err := k.EffectiveEpochIndex.Get(ctx)
		if err != nil {
			return fromVM, err
		}
		if currentEpochIndex < 2 {
			return fromVM, nil
		}

		err = setEpochParticipantsSet(ctx, k, currentEpochIndex)
		if err != nil {
			return fromVM, err
		}

		err = setEpochParticipantsSet(ctx, k, currentEpochIndex-1)
		if err != nil {
			return fromVM, err
		}

		toVM, err := mm.RunMigrations(ctx, configurator, fromVM)
		if err != nil {
			return toVM, err
		}

		k.LogInfo("successfully upgraded", types.Upgrades, "version", UpgradeName)
		return toVM, nil
	}
}

// setSafetyWindow sets the safety_window parameter to 50.
func setSafetyWindow(ctx context.Context, k keeper.Keeper) error {
	params, err := k.GetParams(ctx)
	if err != nil {
		k.LogError("failed to get params during upgrade", types.Upgrades, "error", err)
		return err
	}

	if params.EpochParams == nil {
		k.LogError("epoch params not initialized", types.Upgrades)
		return errors.New("EpochParams are nil")
	}

	params.EpochParams.ConfirmationPocSafetyWindow = 50

	if err := k.SetParams(ctx, params); err != nil {
		k.LogError("failed to set params with safety window", types.Upgrades, "error", err)
		return err
	}

	k.LogInfo("set safety window", types.Upgrades, "safety_window", params.EpochParams.ConfirmationPocSafetyWindow)
	return nil
}

func setEpochParticipantsSet(ctx context.Context, k keeper.Keeper, epochIndex uint64) error {
	epochActiveParticipants, found := k.GetActiveParticipants(ctx, epochIndex)
	if !found {
		return types.ErrEpochNotFound
	}
	return k.SetActiveParticipantsCache(ctx, epochActiveParticipants)
}
