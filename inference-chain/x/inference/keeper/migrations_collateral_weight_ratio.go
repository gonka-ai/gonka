package keeper

import (
	"fmt"

	"cosmossdk.io/store/prefix"
	"github.com/cosmos/cosmos-sdk/runtime"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

var migrationCollateralWeightRatioOne = &types.Decimal{Value: 1, Exponent: 0}

// MigrateCollateralWeightRatio initializes CollateralWeightRatio for existing EpochGroupData and ActiveParticipants.
// This migration is needed because CollateralWeightRatio is a new field.
func (k Keeper) MigrateCollateralWeightRatio(ctx sdk.Context) error {
	k.Logger().Info("migration: initializing collateral weight ratios")

	updatedValidationWeights, err := k.migrateEpochGroupDataCollateralWeightRatios(ctx)
	if err != nil {
		return err
	}

	updatedParticipants, err := k.migrateActiveParticipantsCollateralWeightRatios(ctx)
	if err != nil {
		return err
	}

	k.Logger().Info("migration: finished initializing collateral weight ratios",
		"updated_validation_weights", updatedValidationWeights,
		"updated_active_participants", updatedParticipants,
	)

	return nil
}

// migrateEpochGroupDataCollateralWeightRatios iterates over all stored EpochGroupData entries
// and initializes CollateralWeightRatio for ValidationWeights where the field is missing.
func (k Keeper) migrateEpochGroupDataCollateralWeightRatios(ctx sdk.Context) (int, error) {
	updatedValidationWeights := 0

	epochGroupDataIter, err := k.EpochGroupDataMap.Iterate(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer epochGroupDataIter.Close()

	for ; epochGroupDataIter.Valid(); epochGroupDataIter.Next() {
		key, err := epochGroupDataIter.Key()
		if err != nil {
			return 0, err
		}
		epochGroupData, err := epochGroupDataIter.Value()
		if err != nil {
			return 0, err
		}

		changed := false
		for i := range epochGroupData.ValidationWeights {
			if epochGroupData.ValidationWeights[i].CollateralWeightRatio != nil {
				continue
			}

			epochGroupData.ValidationWeights[i].CollateralWeightRatio = migrationCollateralWeightRatioOne
			updatedValidationWeights++
			changed = true
		}

		if !changed {
			continue
		}

		if err := k.EpochGroupDataMap.Set(ctx, key, epochGroupData); err != nil {
			return 0, err
		}
	}

	return updatedValidationWeights, nil
}

// migrateActiveParticipantsCollateralWeightRatios iterates over all stored ActiveParticipants entries
// and initializes CollateralWeightRatio for participants where the field is missing.
func (k Keeper) migrateActiveParticipantsCollateralWeightRatios(ctx sdk.Context) (int, error) {
	updatedParticipants := 0
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, []byte(types.ActiveParticipantsKeyPrefix))
	iterator := store.Iterator(nil, nil)
	defer iterator.Close()

	for ; iterator.Valid(); iterator.Next() {
		var activeParticipants types.ActiveParticipants
		if err := k.cdc.Unmarshal(iterator.Value(), &activeParticipants); err != nil {
			return 0, fmt.Errorf("failed to unmarshal active participants during collateral weight ratio migration: %w", err)
		}

		changed := false
		for i := range activeParticipants.Participants {
			participant := activeParticipants.Participants[i]
			if participant == nil || participant.CollateralWeightRatio != nil {
				continue
			}

			participant.CollateralWeightRatio = migrationCollateralWeightRatioOne
			updatedParticipants++
			changed = true
		}

		if !changed {
			continue
		}

		bz, err := k.cdc.Marshal(&activeParticipants)
		if err != nil {
			return 0, fmt.Errorf("failed to marshal active participants during collateral weight ratio migration: %w", err)
		}
		store.Set(iterator.Key(), bz)
	}

	return updatedParticipants, nil
}
