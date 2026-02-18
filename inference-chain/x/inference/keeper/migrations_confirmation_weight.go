package keeper

import (
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

// MigrateConfirmationWeights initializes ConfirmationWeight for existing EpochGroupData.
// This migration is needed for the v0.2.5 upgrade because ConfirmationWeight is a new field.
func (k Keeper) MigrateConfirmationWeights(ctx sdk.Context) error {
	k.Logger().Info("migration: initializing confirmation weights for current epoch")

	currentEpochIndex, found := k.GetEffectiveEpochIndex(ctx)
	if !found {
		k.Logger().Error("migration: no current epoch found, skipping")
		return nil
	}

	updatedCount := 0

	epochGroupData, found := k.GetEpochGroupData(ctx, currentEpochIndex, "")
	if !found {
		k.Logger().Error("migration: no epoch group data found for current epoch, skipping")
		return nil
	}

	activeParticipants, found := k.GetActiveParticipants(ctx, currentEpochIndex)
	if !found {
		k.Logger().Error("migration: no active participants found for current epoch, skipping")
		return nil
	}

	activeParticipantToConfirmationWeight := make(map[string]int64)
	for _, participant := range activeParticipants.Participants {
		confirmationWeight := calculatePocParticipatingNodesWeight(participant.MlNodes)
		activeParticipantToConfirmationWeight[participant.Index] = confirmationWeight
	}

	for i := range epochGroupData.ValidationWeights {
		confirmationWeight, ok := activeParticipantToConfirmationWeight[epochGroupData.ValidationWeights[i].MemberAddress]
		if !ok {
			k.Logger().Error("migration: no confirmation weight found for participant",
				"participant", epochGroupData.ValidationWeights[i].MemberAddress)
			continue
		}
		epochGroupData.ValidationWeights[i].ConfirmationWeight = confirmationWeight
		updatedCount++
	}

	k.SetEpochGroupData(ctx, epochGroupData)

	k.Logger().Info("migration: finished initializing confirmation weights",
		"epoch", currentEpochIndex,
		"updated", updatedCount)

	return nil
}

// calculatePocParticipatingNodesWeight calculates the total weight of nodes participating in PoC.
//
// NOTE: This logic is intentionally duplicated from the epoch group implementation in
// x/inference/epochgroup/epoch_group.go. Any changes to the weight-calculation logic here
// must also be applied there (and vice versa) to keep confirmation and validation weights
// consistent across the codebase.
func calculatePocParticipatingNodesWeight(mlNodes []*types.ModelMLNodes) int64 {
	totalWeight := int64(0)

	for _, modelNodes := range mlNodes {
		if modelNodes == nil {
			continue
		}

		for _, node := range modelNodes.MlNodes {
			if node == nil {
				continue
			}

			// POC_SLOT is at index 1 (second timeslot)
			// false => participant in PoC phase, true => continues inference during PoC
			if len(node.TimeslotAllocation) > 1 && !node.TimeslotAllocation[1] {
				totalWeight += node.PocWeight
			}
		}
	}

	return totalWeight
}
