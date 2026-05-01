package v0_2_12_poc_bootstrap

import (
	"context"

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

		seedPreservedNodes(ctx, k)

		toVM, err := mm.RunMigrations(ctx, configurator, fromVM)
		if err != nil {
			return toVM, err
		}

		k.Logger().Info("successfully upgraded to " + UpgradeName)
		return toVM, nil
	}
}

// seedPreservedNodes bootstraps the preserved nodes deadlock by:
// 1. Storing fake seeds for the upcoming epoch so ComputeNewWeights can include all participants
// 2. Modifying ActiveParticipants for the effective epoch to set TimeslotAllocation[1]=true
// 3. Adding ValidationWeights to the parent EpochGroupData so the selection loop can iterate
func seedPreservedNodes(ctx context.Context, k keeper.Keeper) {
	allChainParticipants := k.GetAllParticipant(ctx)
	if len(allChainParticipants) == 0 {
		k.LogWarn("seedPreservedNodes: no participants found, skipping", types.Upgrades)
		return
	}
	k.LogInfo("seedPreservedNodes: found participants", types.Upgrades, "count", len(allChainParticipants))

	// 1. Store seeds for upcoming epoch so ComputeNewWeights includes all participants.
	//    The seed value and signature are placeholders — ComputeNewWeights only checks existence.
	upcomingEpoch, found := k.GetUpcomingEpoch(ctx)
	if found && upcomingEpoch != nil {
		for _, p := range allChainParticipants {
			seed := types.RandomSeed{
				Participant: p.Address,
				EpochIndex:  upcomingEpoch.Index,
				Signature:   []byte{0x01},
			}
			if err := k.SetRandomSeed(ctx, seed); err != nil {
				k.LogError("seedPreservedNodes: failed to set random seed", types.Upgrades,
					"participant", p.Address, "epoch", upcomingEpoch.Index, "error", err)
			} else {
				k.LogInfo("seedPreservedNodes: seeded random seed", types.Upgrades,
					"participant", p.Address, "epoch", upcomingEpoch.Index)
			}
		}
	} else {
		k.LogWarn("seedPreservedNodes: no upcoming epoch found, skipping seed seeding", types.Upgrades)
	}

	// 2. Get effective epoch index — this is the epoch whose ActiveParticipants the
	//    NEXT epoch's PoC selection reads via GetPreservedNodesByParticipant.
	effectiveEpochIndex, found := k.GetEffectiveEpochIndex(ctx)
	if !found {
		k.LogWarn("seedPreservedNodes: no effective epoch index found, skipping", types.Upgrades)
		return
	}
	k.LogInfo("seedPreservedNodes: effective epoch index", types.Upgrades, "epochIndex", effectiveEpochIndex)

	// Read existing ActiveParticipants (may be empty or have participants without POC_SLOT).
	existingAP, _ := k.GetActiveParticipants(ctx, effectiveEpochIndex)

	// Build lookup map by address for fast update.
	apMap := make(map[string]*types.ActiveParticipant)
	for _, p := range existingAP.Participants {
		apMap[p.Index] = p
	}

	// Build updated participant list: set TimeslotAllocation[1]=true for all ML nodes.
	updatedParticipants := make([]*types.ActiveParticipant, 0, len(allChainParticipants))
	for _, chainP := range allChainParticipants {
		if existing, ok := apMap[chainP.Address]; ok {
			// Update existing: ensure POC_SLOT is true for all ML nodes.
			if len(existing.MlNodes) == 0 {
				existing.MlNodes = []*types.ModelMLNodes{
					{MlNodes: []*types.MLNodeInfo{
						{NodeId: "node1", PocWeight: 1, TimeslotAllocation: []bool{true, true}},
					}},
				}
			} else {
				for _, modelNodes := range existing.MlNodes {
					for _, mlNode := range modelNodes.MlNodes {
						for len(mlNode.TimeslotAllocation) < 2 {
							mlNode.TimeslotAllocation = append(mlNode.TimeslotAllocation, false)
						}
						mlNode.TimeslotAllocation[1] = true
						if mlNode.PocWeight == 0 {
							mlNode.PocWeight = 1
						}
					}
				}
			}
			updatedParticipants = append(updatedParticipants, existing)
		} else {
			// Create minimal ActiveParticipant with POC_SLOT=true.
			ap := &types.ActiveParticipant{
				Index:        chainP.Address,
				ValidatorKey: chainP.ValidatorKey,
				Weight:       1,
				InferenceUrl: chainP.InferenceUrl,
				MlNodes: []*types.ModelMLNodes{
					{MlNodes: []*types.MLNodeInfo{
						{NodeId: "node1", PocWeight: 1, TimeslotAllocation: []bool{true, true}},
					}},
				},
			}
			updatedParticipants = append(updatedParticipants, ap)
		}
	}

	newAP := types.ActiveParticipants{
		Participants:         updatedParticipants,
		EpochId:              effectiveEpochIndex,
		EpochGroupId:         existingAP.EpochGroupId,
		PocStartBlockHeight:  existingAP.PocStartBlockHeight,
		EffectiveBlockHeight: existingAP.EffectiveBlockHeight,
		CreatedAtBlockHeight: existingAP.CreatedAtBlockHeight,
	}
	if newAP.EpochGroupId == 0 {
		newAP.EpochGroupId = effectiveEpochIndex
	}

	if err := k.SetActiveParticipants(ctx, newAP); err != nil {
		k.LogError("seedPreservedNodes: failed to set active participants", types.Upgrades, "error", err)
		return
	}
	k.LogInfo("seedPreservedNodes: set active participants with POC_SLOT=true", types.Upgrades,
		"epochIndex", effectiveEpochIndex, "count", len(updatedParticipants))

	// 3. Add ValidationWeights to parent EpochGroupData if currently empty.
	//    GetPreviousEpochMLNodesWithInferenceAllocation iterates ValidationWeights of the
	//    current (effective) epoch's parent group to find participants to check.
	parentData, found := k.GetEpochGroupData(ctx, effectiveEpochIndex, "")
	if !found {
		k.LogWarn("seedPreservedNodes: parent epoch group data not found", types.Upgrades, "epochIndex", effectiveEpochIndex)
		return
	}

	if len(parentData.ValidationWeights) == 0 {
		for _, ap := range updatedParticipants {
			parentData.ValidationWeights = append(parentData.ValidationWeights, &types.ValidationWeight{
				MemberAddress: ap.Index,
				Weight:        ap.Weight,
			})
		}
		k.SetEpochGroupData(ctx, parentData)
		k.LogInfo("seedPreservedNodes: added ValidationWeights to parent EpochGroupData", types.Upgrades,
			"epochIndex", effectiveEpochIndex, "count", len(parentData.ValidationWeights))
	} else {
		k.LogInfo("seedPreservedNodes: parent EpochGroupData already has ValidationWeights, skipping", types.Upgrades,
			"epochIndex", effectiveEpochIndex, "count", len(parentData.ValidationWeights))
	}
}
