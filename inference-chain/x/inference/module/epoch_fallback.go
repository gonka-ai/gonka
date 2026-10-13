package inference

import (
	"context"
	"fmt"
	"slices"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

// Epoch fallback: safety mechanism for an epoch transition where PoC validation
// produced zero active participants.
//
// Without a fallback, an empty result wipes the upcoming epoch: no active
// participants, no epoch group members, no voting powers. Since the next PoC
// round derives voting powers from the (now empty) effective epoch group,
// nobody can ever be validated again and the network is permanently stuck.
//
// The fallback re-seats the current epoch's validators into the upcoming epoch,
// with three restrictions so that nobody excluded during the current epoch gets
// a second life:
//  1. Only live SDK-group members are carried. Mid-epoch invalidation and
//     deactivation (invalid inferences, downtime, failed confirmation PoC)
//     remove the member from the group, so removed participants are not live.
//  2. The ExcludedParticipantsMap record for the current epoch is honored.
//  3. The participant's current Status must not be INVALID or INACTIVE.
//
// Carried participants are initialized like freshly validated ones: a new
// ActiveParticipant record is built from scratch (no statistics or per-epoch
// state is copied) and the result flows through the exact same epoch-formation
// pipeline (model assignment, delegation weights, collateral adjustment, power
// capping, epoch group membership) as a normal PoC outcome.

// seatedRawWeight sums the positive PocWeight actually seated for a participant
// after model assignment.
func seatedRawWeight(p *types.ActiveParticipant) int64 {
	var total int64
	for _, modelNodes := range p.MlNodes {
		if modelNodes == nil {
			continue
		}
		for _, node := range modelNodes.MlNodes {
			if node != nil && node.PocWeight > 0 {
				total += node.PocWeight
			}
		}
	}
	return total
}

// participantsWithSeatedWeight keeps only participants that still hold positive
// seated raw weight after the hardware filter. A participant whose every node
// was filtered out must not reach epoch membership, consensus weights, or BLS.
func participantsWithSeatedWeight(participants []*types.ActiveParticipant) []*types.ActiveParticipant {
	kept := make([]*types.ActiveParticipant, 0, len(participants))
	for _, p := range participants {
		if p != nil && len(p.Models) > 0 && seatedRawWeight(p) > 0 {
			kept = append(kept, p)
		}
	}
	return kept
}

func hasFreshSeatedNode(participants []*types.ActiveParticipant, freshNodeIDs map[string]map[string]struct{}) bool {
	for _, participant := range participants {
		if participant == nil {
			continue
		}
		participantFreshNodes := freshNodeIDs[participant.Index]
		if len(participantFreshNodes) == 0 {
			continue
		}
		for _, modelNodes := range participant.MlNodes {
			if modelNodes == nil {
				continue
			}
			for _, node := range modelNodes.MlNodes {
				if node != nil {
					if _, ok := participantFreshNodes[node.NodeId]; ok {
						return true
					}
				}
			}
		}
	}
	return false
}

// seatAndGuardParticipants runs model seating over the computed participant set
// and enforces the post-seating invariant: only participants with positive
// fresh PoC weight survive. If no fresh PoC node survives (ComputeNewWeights
// returned only preserved nodes, or the hardware filter removed every fresh
// assignment), it retries once with the current-epoch fallback carry-over --
// seated and guarded the same way.
// Returns an empty slice only when no current-epoch validator can be carried
// at all (first epoch, no seeds, everyone excluded). The caller then aborts
// formation. If the hardware filter would also wipe the carry, the carry is
// kept as-is so the epoch index can still advance with the old team.
// The bool reports whether the returned set was rebuilt from the current epoch.
func (am AppModule) seatAndGuardParticipants(
	ctx context.Context,
	upcomingEpoch types.Epoch,
	fresh []*types.ActiveParticipant,
	freshNodeIDs map[string]map[string]struct{},
) ([]*types.ActiveParticipant, bool) {
	assigner := NewModelAssigner(am.keeper, am.keeper)
	seat := func(participants []*types.ActiveParticipant) []*types.ActiveParticipant {
		if len(participants) == 0 {
			return nil
		}
		assigner.setModelsForParticipants(ctx, participants, upcomingEpoch)
		return participantsWithSeatedWeight(participants)
	}

	seated := seat(fresh)
	if len(seated) > 0 && hasFreshSeatedNode(seated, freshNodeIDs) {
		if len(seated) < len(fresh) {
			am.LogWarn("seatAndGuardParticipants: removed participants without seated weight", types.PoC,
				"upcomingEpoch.Index", upcomingEpoch.Index,
				"before", len(fresh), "after", len(seated))
		}
		return seated, false
	}

	// Rebuild from the current epoch when no fresh node survives or when the
	// final weight check requests fallback.
	am.LogError("seatAndGuardParticipants: rebuilding participants from current epoch", types.PoC,
		"upcomingEpoch.Index", upcomingEpoch.Index,
		"computed", len(fresh),
		"seated", len(seated))
	seated = seat(am.fallbackActiveParticipantsFromCurrentEpoch(ctx, upcomingEpoch))
	if len(seated) > 0 {
		return seated, true
	}

	// seat() mutates its input; rebuild the carry. If hardware would wipe it
	// too, keep it anyway so the epoch clock still advances with the old team.
	unfiltered := am.fallbackActiveParticipantsFromCurrentEpoch(ctx, upcomingEpoch)
	if len(unfiltered) == 0 {
		am.LogError("seatAndGuardParticipants: fallback produced no seated participants; aborting epoch formation", types.PoC,
			"upcomingEpoch.Index", upcomingEpoch.Index)
		return nil, true
	}
	for _, p := range unfiltered {
		_ = validatedModelNodes(p)
	}
	am.LogError("seatAndGuardParticipants: hardware filter zeroed fallback carry; keeping current-epoch assignments so the epoch can advance", types.PoC,
		"upcomingEpoch.Index", upcomingEpoch.Index, "participants", len(unfiltered))
	return unfiltered, true
}

// applyZeroTrustFallback prevents a positive-real-weight epoch from persisting
// with no validation, governance, or BLS trust. It is an explicit fail-open
// fallback: every trust consumer sees the same persisted CapWeight values.
func (am AppModule) applyZeroTrustFallback(
	ctx context.Context,
	epochIndex uint64,
	participants []*types.ActiveParticipant,
) bool {
	hasRealWeight := false
	for _, participant := range participants {
		if participant == nil {
			continue
		}
		if participant.CapWeight > 0 {
			return false
		}
		if participant.Weight > 0 {
			hasRealWeight = true
		}
	}
	if !hasRealWeight {
		return false
	}

	for _, participant := range participants {
		if participant == nil {
			continue
		}
		if participant.Weight > 0 {
			participant.CapWeight = participant.Weight
		} else {
			participant.CapWeight = 0
		}
	}

	am.LogError("applyZeroTrustFallback: restoring real weight because every trust weight is zero", types.PoC,
		"epochIndex", epochIndex,
		"participants", len(participants))
	sdk.UnwrapSDKContext(ctx).EventManager().EmitEvent(sdk.NewEvent(
		"zero_trust_fallback_applied",
		sdk.NewAttribute("epoch", fmt.Sprintf("%d", epochIndex)),
		sdk.NewAttribute("participants", fmt.Sprintf("%d", len(participants))),
	))
	return true
}

// fallbackActiveParticipantsFromCurrentEpoch rebuilds the active participant
// list for upcomingEpoch from the current (about-to-end) epoch group.
// Returns nil/empty when no participant can be safely carried over.
func (am AppModule) fallbackActiveParticipantsFromCurrentEpoch(ctx context.Context, upcomingEpoch types.Epoch) []*types.ActiveParticipant {
	if upcomingEpoch.Index <= 1 {
		am.LogWarn("EpochFallback: not applicable for the first epoch", types.PoC,
			"upcomingEpoch.Index", upcomingEpoch.Index)
		return nil
	}

	rootData, liveSet, err := am.keeper.GetRootGroupDataWithLiveMembers(ctx)
	if err != nil {
		am.LogError("EpochFallback: unable to load current epoch group", types.PoC, "error", err.Error())
		return nil
	}
	if rootData.EpochIndex != upcomingEpoch.Index-1 {
		am.LogError("EpochFallback: current epoch group does not precede upcoming epoch", types.PoC,
			"currentEpochGroup.EpochIndex", rootData.EpochIndex,
			"upcomingEpoch.Index", upcomingEpoch.Index)
		return nil
	}

	modelNodesByParticipant := am.currentEpochModelNodes(ctx, rootData)

	participantsByAddr := make(map[string]*types.ActiveParticipant)
	for _, vw := range rootData.ValidationWeights {
		if vw == nil || vw.MemberAddress == "" {
			continue
		}
		addr := vw.MemberAddress
		if _, dup := participantsByAddr[addr]; dup {
			continue
		}

		if !liveSet[addr] {
			am.LogWarn("EpochFallback: skipping participant removed from current epoch group", types.PoC,
				"participant", addr)
			continue
		}
		if am.wasExcludedInEpoch(ctx, rootData.EpochIndex, addr) {
			am.LogWarn("EpochFallback: skipping participant excluded during current epoch", types.PoC,
				"participant", addr, "epochIndex", rootData.EpochIndex)
			continue
		}

		participant, found := am.keeper.GetParticipant(ctx, addr)
		if !found {
			am.LogError("EpochFallback: participant record not found", types.PoC, "participant", addr)
			continue
		}
		if participant.Status == types.ParticipantStatus_INVALID ||
			participant.Status == types.ParticipantStatus_INACTIVE {
			am.LogWarn("EpochFallback: skipping participant with non-carryable status", types.PoC,
				"participant", addr, "status", participant.Status)
			continue
		}
		if participant.ValidatorKey == "" {
			am.LogWarn("EpochFallback: skipping participant without validator key", types.PoC,
				"participant", addr)
			continue
		}

		seed, ok := am.fallbackSeed(ctx, upcomingEpoch.Index, rootData.EpochIndex, addr)
		if !ok {
			am.LogWarn("EpochFallback: skipping participant without any usable seed", types.PoC,
				"participant", addr)
			continue
		}

		models, mlNodes := buildFallbackModelNodes(modelNodesByParticipant[addr])

		activeParticipant := &types.ActiveParticipant{
			Index:        addr,
			ValidatorKey: participant.ValidatorKey,
			InferenceUrl: participant.InferenceUrl,
			Seed:         seed,
			Models:       models,
			MlNodes:      mlNodes,
		}
		activeParticipant.Weight = RecalculateWeight(activeParticipant)
		if activeParticipant.Weight <= 0 {
			// No per-model nodes recovered from subgroup data. Carry the root
			// consensus weight so the participant still counts toward the set;
			// the downstream weight pipeline recomputes final weights anyway.
			activeParticipant.Weight = vw.Weight
		}
		if activeParticipant.Weight <= 0 {
			am.LogWarn("EpochFallback: skipping participant with non-positive weight", types.PoC,
				"participant", addr)
			continue
		}

		participantsByAddr[addr] = activeParticipant
		am.LogInfo("EpochFallback: carrying participant into upcoming epoch", types.PoC,
			"participant", addr,
			"weight", activeParticipant.Weight,
			"models", models)
	}

	addrs := make([]string, 0, len(participantsByAddr))
	for addr := range participantsByAddr {
		addrs = append(addrs, addr)
	}
	slices.Sort(addrs)

	result := make([]*types.ActiveParticipant, 0, len(addrs))
	for _, addr := range addrs {
		result = append(result, participantsByAddr[addr])
	}

	am.LogInfo("EpochFallback: summary", types.PoC,
		"upcomingEpoch.Index", upcomingEpoch.Index,
		"candidates", len(rootData.ValidationWeights),
		"carried", len(result))

	return result
}

// currentEpochModelNodes rebuilds per-participant, per-model MLNode info from
// the current epoch's model subgroups. Nodes are copied fresh (id, throughput,
// PoC weight only) so no scheduling or per-epoch state leaks into the new epoch.
func (am AppModule) currentEpochModelNodes(
	ctx context.Context,
	rootData types.EpochGroupData,
) map[string]map[string][]*types.MLNodeInfo {
	result := make(map[string]map[string][]*types.MLNodeInfo)

	models := slices.Clone(rootData.SubGroupModels)
	slices.Sort(models)

	for _, modelId := range models {
		subData, found := am.keeper.GetEpochGroupData(ctx, rootData.EpochIndex, modelId)
		if !found {
			am.LogWarn("EpochFallback: model subgroup data not found", types.PoC,
				"epochIndex", rootData.EpochIndex, "modelId", modelId)
			continue
		}
		for _, vw := range subData.ValidationWeights {
			if vw == nil || vw.MemberAddress == "" {
				continue
			}
			seenNodeIds := make(map[string]bool, len(vw.MlNodes))
			for _, node := range vw.MlNodes {
				if node == nil || node.NodeId == "" || seenNodeIds[node.NodeId] {
					continue
				}
				seenNodeIds[node.NodeId] = true
				if result[vw.MemberAddress] == nil {
					result[vw.MemberAddress] = make(map[string][]*types.MLNodeInfo)
				}
				result[vw.MemberAddress][modelId] = append(result[vw.MemberAddress][modelId], &types.MLNodeInfo{
					NodeId:     node.NodeId,
					Throughput: node.Throughput,
					PocWeight:  node.PocWeight,
				})
			}
		}
	}

	return result
}

// buildFallbackModelNodes converts a model->nodes map into the parallel
// Models/MlNodes arrays used by ActiveParticipant, in deterministic order.
func buildFallbackModelNodes(modelNodes map[string][]*types.MLNodeInfo) ([]string, []*types.ModelMLNodes) {
	if len(modelNodes) == 0 {
		return nil, nil
	}

	models := make([]string, 0, len(modelNodes))
	for modelId := range modelNodes {
		models = append(models, modelId)
	}
	slices.Sort(models)

	mlNodes := make([]*types.ModelMLNodes, 0, len(models))
	for _, modelId := range models {
		mlNodes = append(mlNodes, &types.ModelMLNodes{MlNodes: modelNodes[modelId]})
	}
	return models, mlNodes
}

// fallbackSeed picks the seed for a carried participant. A seed submitted for
// the upcoming epoch is preferred. If none exists (likely, given the epoch
// formation just failed), the current epoch's seed is reused: its value may
// already be revealed via a prior claim, which weakens validation-sampling
// unpredictability for this participant, but that is an acceptable trade-off
// for keeping the chain alive.
func (am AppModule) fallbackSeed(
	ctx context.Context,
	upcomingEpochIndex, currentEpochIndex uint64,
	address string,
) (*types.RandomSeed, bool) {
	if seed, found := am.keeper.GetRandomSeed(ctx, upcomingEpochIndex, address); found {
		return &seed, true
	}
	seed, found := am.keeper.GetRandomSeed(ctx, currentEpochIndex, address)
	if !found {
		return nil, false
	}
	am.LogWarn("EpochFallback: reusing current-epoch seed for upcoming epoch", types.PoC,
		"participant", address,
		"currentEpochIndex", currentEpochIndex,
		"upcomingEpochIndex", upcomingEpochIndex)
	seed.EpochIndex = upcomingEpochIndex
	return &seed, true
}

// wasExcludedInEpoch reports whether the participant has an exclusion record
// (invalidation, downtime, failed confirmation PoC) for the given epoch.
func (am AppModule) wasExcludedInEpoch(ctx context.Context, epochIndex uint64, address string) bool {
	accAddr, err := sdk.AccAddressFromBech32(address)
	if err != nil {
		am.LogError("EpochFallback: unable to parse participant address for exclusion check", types.PoC,
			"participant", address, "error", err.Error())
		return false
	}
	has, err := am.keeper.ExcludedParticipantsMap.Has(ctx, collections.Join(epochIndex, accAddr))
	if err != nil {
		am.LogError("EpochFallback: exclusion lookup failed", types.PoC,
			"participant", address, "error", err.Error())
		return false
	}
	return has
}
