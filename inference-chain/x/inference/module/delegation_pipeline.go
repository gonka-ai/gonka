package inference

import (
	"cmp"
	"context"
	"fmt"
	"math"
	"math/bits"
	"slices"
	"strconv"
	"strings"

	mathsdk "cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

// protoDecToLegacy converts a proto Decimal to LegacyDec, returning zero on nil or error.
// Follows the pattern from GetWeightScaleFactorDec in params.go.
func protoDecToLegacy(d *types.Decimal) mathsdk.LegacyDec {
	if d == nil || (d.Value == 0 && d.Exponent == 0) {
		return mathsdk.LegacyZeroDec()
	}
	dec, err := d.ToLegacyDec()
	if err != nil {
		return mathsdk.LegacyZeroDec()
	}
	return dec
}

// buildDelegationWeightCalculator constructs a DelegationWeightCalculator from
// the current epoch pipeline state.
func (am AppModule) buildDelegationWeightCalculator(
	ctx context.Context,
	activeParticipants []*types.ActiveParticipant,
	coefficients map[string]mathsdk.LegacyDec,
	params types.Params,
) *DelegationWeightCalculator {
	nextEpochDelegations, nextEpochRefusals, found := am.loadRegularDelegationSnapshotState(ctx)
	if !found {
		am.LogError("regular delegation snapshot not found", types.PoC, "context", "onEndOfPoCValidationStage")
		nextEpochDelegations = map[string]map[string]string{}
		nextEpochRefusals = map[string]map[string]bool{}
	}
	prevState := am.getEffectiveValidationBaseState(ctx)
	consensusWeights, totalWeight := prevState.weights, prevState.totalWeight
	initialModelID := params.GetDelegationParams().GetInitialModelId()
	groups := buildGroupData(activeParticipants, coefficients, initialModelID, am)

	upcomingActiveParticipants := make(map[string]bool, len(activeParticipants))
	for _, p := range activeParticipants {
		upcomingActiveParticipants[p.Index] = true
	}

	return &DelegationWeightCalculator{
		Groups:                     groups,
		ConsensusWeights:           consensusWeights,
		UpcomingActiveParticipants: upcomingActiveParticipants,
		TotalNetworkWeight:         totalWeight,
		Delegations:                nextEpochDelegations,
		Refusals:                   nextEpochRefusals,
		Params:                     buildWeightParams(params),
	}
}

type epochParticipationState struct {
	calculator              *DelegationWeightCalculator
	eligibleModels          []string
	participationByModel    map[string]map[string]ParticipationMode
	bootstrapPenaltyByModel map[string]map[string]BootstrapPenaltyMode
}

func buildParticipationByModel(
	calculator *DelegationWeightCalculator,
	eligibleModels []string,
) map[string]map[string]ParticipationMode {
	participationByModel := make(map[string]map[string]ParticipationMode, len(eligibleModels))
	for _, modelID := range eligibleModels {
		participationByModel[modelID] = calculator.ResolveGroupParticipation(modelID)
	}
	return participationByModel
}

func (am AppModule) prepareEpochParticipationState(
	ctx context.Context,
	activeParticipants []*types.ActiveParticipant,
	params types.Params,
	pocStageStartHeight int64,
) (*epochParticipationState, error) {
	coefficients := modelCoefficients(params.PocParams)
	calculator := am.buildDelegationWeightCalculator(ctx, activeParticipants, coefficients, params)
	eligibleModels := calculator.EligibleGroups()
	participationByModel := buildParticipationByModel(calculator, eligibleModels)

	state := &epochParticipationState{
		calculator:              calculator,
		eligibleModels:          eligibleModels,
		participationByModel:    participationByModel,
		bootstrapPenaltyByModel: map[string]map[string]BootstrapPenaltyMode{},
	}

	bootstrapInputs, found := am.loadBootstrapPenaltyInputs(ctx)
	if !found {
		return state, nil
	}

	bootstrapPenaltyByModel, err := am.resolveBootstrapPenaltyModes(
		ctx,
		activeParticipants,
		pocStageStartHeight,
		bootstrapInputs,
	)
	if err != nil {
		return nil, err
	}
	state.bootstrapPenaltyByModel = bootstrapPenaltyByModel

	return state, nil
}

// buildGroupData constructs GroupData from activeParticipants after model assignment.
// Each participant's Models[] and MlNodes[] are parallel arrays.
// initialModelID identifies the founding model exempt from the group cap.
func buildGroupData(
	activeParticipants []*types.ActiveParticipant,
	coefficients map[string]mathsdk.LegacyDec,
	initialModelID string,
	logger types.InferenceLogger,
) map[string]*GroupData {
	groups := make(map[string]*GroupData)

	for _, p := range activeParticipants {
		for i, modelID := range p.Models {
			g, ok := groups[modelID]
			if !ok {
				coeff, hasCoeff := coefficients[modelID]
				if !hasCoeff {
					coeff = mathsdk.LegacyOneDec()
				}
				g = &GroupData{
					MemberPocWeights: make(map[string]int64),
					ConsensusKoeff:   coeff,
					IsInitialGroup:   modelID == initialModelID,
				}
				groups[modelID] = g
			}
			g.Members = append(g.Members, p.Index)
			if i < len(p.MlNodes) && p.MlNodes[i] != nil {
				g.MemberPocWeights[p.Index] = sumNodeWeights(p.MlNodes[i].MlNodes)
			} else if logger != nil {
				logger.LogWarn("buildGroupData: Models/MlNodes parallel array mismatch", types.PoC,
					"participant", p.Index, "modelIndex", i, "modelsLen", len(p.Models), "mlNodesLen", len(p.MlNodes))
			}
		}
	}

	return groups
}

func sumNodeWeights(nodes []*types.MLNodeInfo) int64 {
	total := int64(0)
	for _, node := range nodes {
		if node != nil {
			total += node.PocWeight
		}
	}
	return total
}

func parseRegularDelegationSnapshot(snapshot types.DelegationSnapshot) (
	delegations map[string]map[string]string,
	refusals map[string]map[string]bool,
) {
	delegations = make(map[string]map[string]string)
	refusals = make(map[string]map[string]bool)

	for _, d := range snapshot.Delegations {
		if delegations[d.ModelId] == nil {
			delegations[d.ModelId] = make(map[string]string)
		}
		delegations[d.ModelId][d.Delegator] = d.DelegateTo
	}
	for _, r := range snapshot.Refusals {
		if refusals[r.ModelId] == nil {
			refusals[r.ModelId] = make(map[string]bool)
		}
		refusals[r.ModelId][r.Participant] = true
	}
	return delegations, refusals
}

func parseBootstrapDelegationSnapshot(snapshot types.BootstrapDelegationSnapshot) (
	delegations map[string]map[string]string,
	intents map[string]map[string]bool,
) {
	delegations = make(map[string]map[string]string)
	intents = make(map[string]map[string]bool)

	for _, d := range snapshot.Delegations {
		if delegations[d.ModelId] == nil {
			delegations[d.ModelId] = make(map[string]string)
		}
		delegations[d.ModelId][d.Delegator] = d.DelegateTo
	}
	for _, i := range snapshot.Intents {
		if intents[i.ModelId] == nil {
			intents[i.ModelId] = make(map[string]bool)
		}
		intents[i.ModelId][i.Participant] = true
	}

	return delegations, intents
}

func (am AppModule) loadRegularDelegationSnapshotState(
	ctx context.Context,
) (
	map[string]map[string]string,
	map[string]map[string]bool,
	bool,
) {
	snapshot, found := am.keeper.GetDelegationSnapshot(ctx)
	if !found {
		return nil, nil, false
	}
	delegations, refusals := parseRegularDelegationSnapshot(snapshot)
	return delegations, refusals, true
}

// captureDelegationSnapshot stores the frozen delegation state used later at
// validation start. Intents are intentionally excluded from this snapshot.
func (am AppModule) captureDelegationSnapshot(ctx context.Context, blockHeight, pocStageStartBlockHeight int64) {
	snapshot, err := am.buildDelegationSnapshot(ctx, blockHeight, pocStageStartBlockHeight)
	if err != nil {
		am.LogError("captureDelegationSnapshot: failed to build", types.PoC, "error", err)
		return
	}

	if err := am.keeper.SetDelegationSnapshot(ctx, snapshot); err != nil {
		am.LogError("captureDelegationSnapshot: failed to store", types.PoC, "error", err)
		return
	}

	am.LogInfo("captureDelegationSnapshot: stored delegation snapshot", types.PoC,
		"height", blockHeight,
		"delegations", len(snapshot.Delegations),
		"refusals", len(snapshot.Refusals))
}

// buildDelegationSnapshot captures delegations and refusals from N-1 effective
// participants plus current-stage PoC store committers.
func (am AppModule) buildDelegationSnapshot(ctx context.Context, blockHeight, pocStageStartBlockHeight int64) (types.DelegationSnapshot, error) {
	params, err := am.keeper.GetParams(ctx)
	if err != nil {
		return types.DelegationSnapshot{}, err
	}

	effectiveState := am.getEffectiveValidationBaseState(ctx)
	committers, err := am.keeper.GetAllPoCV2StoreCommitsForStage(ctx, pocStageStartBlockHeight)
	if err != nil {
		return types.DelegationSnapshot{}, err
	}

	addrs := make(map[string]struct{}, len(effectiveState.participants)+len(committers))
	for _, p := range effectiveState.participants {
		addrs[p.Index] = struct{}{}
	}
	for k := range committers {
		addrs[k.ParticipantAddress] = struct{}{}
	}

	modelIDs := approvedModelIDs(params.PocParams)
	delegationEntries, refusalEntries := am.loadFilteredDelegationSnapshotState(ctx, addrs, modelIDs)

	return types.DelegationSnapshot{
		SnapshotHeight: blockHeight,
		Delegations:    delegationEntries,
		Refusals:       refusalEntries,
	}, nil
}

// captureBootstrapDelegationSnapshot stores the filtered bootstrap delegation and
// intent state needed to evaluate pre-eligibility for approved models that are
// not already active in the effective epoch.
func (am AppModule) captureBootstrapDelegationSnapshot(ctx context.Context, blockHeight int64) {
	snapshot, err := am.buildBootstrapDelegationSnapshot(ctx, blockHeight)
	if err != nil {
		am.LogError("captureBootstrapDelegationSnapshot: failed to build", types.PoC, "error", err)
		return
	}

	if err := am.keeper.SetBootstrapDelegationSnapshot(ctx, snapshot); err != nil {
		am.LogError("captureBootstrapDelegationSnapshot: failed to store", types.PoC, "error", err)
		return
	}

	am.emitBootstrapPreEligibilityEvents(ctx, snapshot)
	am.LogInfo("captureBootstrapDelegationSnapshot: stored bootstrap snapshot", types.PoC,
		"height", blockHeight,
		"delegations", len(snapshot.Delegations),
		"intents", len(snapshot.Intents),
		"bootstrapModels", len(snapshot.Preeligibility))
}

func (am AppModule) buildBootstrapDelegationSnapshot(
	ctx context.Context,
	blockHeight int64,
) (
	types.BootstrapDelegationSnapshot,
	error,
) {
	params, err := am.keeper.GetParams(ctx)
	if err != nil {
		return types.BootstrapDelegationSnapshot{}, err
	}

	baseState := am.getEffectiveValidationBaseState(ctx)
	effectiveParticipants := baseState.participants
	consensusWeights := baseState.weights
	totalNetworkWeight := baseState.totalWeight
	// Active = models with existing voting powers. Must match computeStoreCommitVotingPowers.
	activeModels := make(map[string]bool)
	for _, mvp := range baseState.existingModelVotingPowers {
		if mvp != nil && mvp.ModelId != "" {
			activeModels[mvp.ModelId] = true
		}
	}
	bootstrapModelIDs := bootstrapCandidateModelIDs(params.PocParams, activeModels)

	bootstrapDelegationEntries,
		bootstrapIntentEntries,
		bootstrapDelegations,
		bootstrapIntents := am.loadFilteredBootstrapState(
		ctx,
		effectiveParticipants,
		bootstrapModelIDs,
	)
	calculator := buildBootstrapPreEligibilityCalculator(
		consensusWeights,
		totalNetworkWeight,
		bootstrapModelIDs,
		bootstrapDelegations,
		bootstrapIntents,
		params,
	)
	results := buildBootstrapPreEligibilityResults(calculator, bootstrapModelIDs)

	return types.BootstrapDelegationSnapshot{
		SnapshotHeight:     blockHeight,
		Delegations:        bootstrapDelegationEntries,
		Intents:            bootstrapIntentEntries,
		TotalNetworkWeight: totalNetworkWeight,
		Preeligibility:     results,
	}, nil
}

func (am AppModule) getEpochZeroValidationBaseState(ctx context.Context) effectiveValidationBaseState {
	epochGroupData, found := am.keeper.GetEpochGroupData(ctx, 0, "")
	if !found {
		return effectiveValidationBaseState{
			weights: map[string]int64{},
		}
	}

	participants := make([]*types.ActiveParticipant, 0, len(epochGroupData.ValidationWeights))
	consensusWeights := make(map[string]int64, len(epochGroupData.ValidationWeights))
	totalNetworkWeight := int64(0)

	for _, validationWeight := range epochGroupData.ValidationWeights {
		if validationWeight == nil {
			continue
		}
		participants = append(participants, &types.ActiveParticipant{
			Index:  validationWeight.MemberAddress,
			Weight: validationWeight.Weight,
		})
		consensusWeights[validationWeight.MemberAddress] = validationWeight.Weight
		totalNetworkWeight += validationWeight.Weight
	}

	return effectiveValidationBaseState{
		participants: participants,
		weights:      consensusWeights,
		totalWeight:  totalNetworkWeight,
	}
}

func bootstrapCandidateModelIDs(
	pocParams *types.PocParams,
	activeModels map[string]bool,
) []string {
	if pocParams == nil {
		return nil
	}

	candidates := make([]string, 0, len(pocParams.GetModelConfigs()))
	for _, modelConfig := range pocParams.GetModelConfigs() {
		if modelConfig == nil || modelConfig.ModelId == "" {
			continue
		}
		if activeModels[modelConfig.ModelId] {
			continue
		}
		candidates = append(candidates, modelConfig.ModelId)
	}

	slices.Sort(candidates)
	return candidates
}

func approvedModelIDs(pocParams *types.PocParams) []string {
	if pocParams == nil {
		return nil
	}

	models := make([]string, 0, len(pocParams.GetModelConfigs()))
	for _, modelConfig := range pocParams.GetModelConfigs() {
		if modelConfig == nil || modelConfig.ModelId == "" {
			continue
		}
		models = append(models, modelConfig.ModelId)
	}

	slices.Sort(models)
	return models
}

func (am AppModule) loadFilteredDelegationSnapshotState(
	ctx context.Context,
	addrs map[string]struct{},
	modelIDs []string,
) ([]*types.PoCDelegation, []*types.PoCRefusal) {
	delegationEntries := make([]*types.PoCDelegation, 0)
	refusalEntries := make([]*types.PoCRefusal, 0)

	sortedAddrs := make([]string, 0, len(addrs))
	for addr := range addrs {
		sortedAddrs = append(sortedAddrs, addr)
	}
	slices.Sort(sortedAddrs)

	for _, addr := range sortedAddrs {
		for _, modelID := range modelIDs {
			delegation, found := am.keeper.GetPoCDelegation(ctx, modelID, addr)
			if found {
				delegationCopy := delegation
				delegationEntries = append(delegationEntries, &delegationCopy)
			}

			if am.keeper.HasPoCRefusal(ctx, modelID, addr) {
				refusalEntries = append(refusalEntries, &types.PoCRefusal{
					ModelId:     modelID,
					Participant: addr,
				})
			}
		}
	}

	slices.SortFunc(delegationEntries, func(a, b *types.PoCDelegation) int {
		return cmp.Or(
			cmp.Compare(a.ModelId, b.ModelId),
			cmp.Compare(a.Delegator, b.Delegator),
		)
	})
	slices.SortFunc(refusalEntries, func(a, b *types.PoCRefusal) int {
		return cmp.Or(
			cmp.Compare(a.ModelId, b.ModelId),
			cmp.Compare(a.Participant, b.Participant),
		)
	})

	return delegationEntries, refusalEntries
}

func (am AppModule) loadFilteredBootstrapState(
	ctx context.Context,
	effectiveParticipants []*types.ActiveParticipant,
	bootstrapModelIDs []string,
) (
	[]*types.PoCDelegation,
	[]*types.PoCDirectIntent,
	map[string]map[string]string,
	map[string]map[string]bool,
) {
	delegationEntries := make([]*types.PoCDelegation, 0)
	intentEntries := make([]*types.PoCDirectIntent, 0)
	delegations := make(map[string]map[string]string)
	intents := make(map[string]map[string]bool)

	for _, participant := range effectiveParticipants {
		for _, modelID := range bootstrapModelIDs {
			delegation, found := am.keeper.GetPoCDelegation(ctx, modelID, participant.Index)
			if found {
				if delegations[modelID] == nil {
					delegations[modelID] = make(map[string]string)
				}
				delegations[modelID][participant.Index] = delegation.DelegateTo
				delegationCopy := delegation
				delegationEntries = append(delegationEntries, &delegationCopy)
			}

			if am.keeper.HasPoCDirectIntent(ctx, modelID, participant.Index) {
				if intents[modelID] == nil {
					intents[modelID] = make(map[string]bool)
				}
				intents[modelID][participant.Index] = true
				intentEntries = append(intentEntries, &types.PoCDirectIntent{
					ModelId:     modelID,
					Participant: participant.Index,
				})
			}
		}
	}

	slices.SortFunc(delegationEntries, func(a, b *types.PoCDelegation) int {
		return cmp.Or(
			cmp.Compare(a.ModelId, b.ModelId),
			cmp.Compare(a.Delegator, b.Delegator),
		)
	})
	slices.SortFunc(intentEntries, func(a, b *types.PoCDirectIntent) int {
		return cmp.Or(
			cmp.Compare(a.ModelId, b.ModelId),
			cmp.Compare(a.Participant, b.Participant),
		)
	})

	return delegationEntries, intentEntries, delegations, intents
}

func (am AppModule) loadBootstrapSnapshotState(ctx context.Context) (
	types.BootstrapDelegationSnapshot,
	map[string]map[string]string,
	map[string]map[string]bool,
	bool,
) {
	snapshot, found := am.keeper.GetBootstrapDelegationSnapshot(ctx)
	if !found {
		return types.BootstrapDelegationSnapshot{}, nil, nil, false
	}

	delegations, intents := parseBootstrapDelegationSnapshot(snapshot)
	return snapshot, delegations, intents, true
}

func buildBootstrapPreEligibilityCalculator(
	consensusWeights map[string]int64,
	totalNetworkWeight int64,
	bootstrapModelIDs []string,
	bootstrapDelegations map[string]map[string]string,
	bootstrapIntents map[string]map[string]bool,
	params types.Params,
) *DelegationWeightCalculator {
	coefficients := modelCoefficients(params.PocParams)
	groups := make(map[string]*GroupData, len(bootstrapModelIDs))
	for _, modelID := range bootstrapModelIDs {
		memberSet := bootstrapIntents[modelID]
		members := make([]string, 0, len(memberSet))
		for participant := range memberSet {
			members = append(members, participant)
		}
		slices.Sort(members)

		coeff, ok := coefficients[modelID]
		if !ok {
			coeff = mathsdk.LegacyOneDec()
		}
		groups[modelID] = &GroupData{
			Members:          members,
			MemberPocWeights: make(map[string]int64),
			ConsensusKoeff:   coeff,
		}
	}

	return &DelegationWeightCalculator{
		Groups:             groups,
		ConsensusWeights:   consensusWeights,
		TotalNetworkWeight: totalNetworkWeight,
		Delegations:        bootstrapDelegations,
		Refusals:           map[string]map[string]bool{},
		Params:             buildWeightParams(params),
	}
}

func buildBootstrapPreEligibilityResults(
	calculator *DelegationWeightCalculator,
	bootstrapModelIDs []string,
) []*types.BootstrapModelPreEligibility {
	results := make([]*types.BootstrapModelPreEligibility, 0, len(bootstrapModelIDs))
	for _, modelID := range bootstrapModelIDs {
		intentHostCount := int64(0)
		intentWeight := int64(0)
		group := calculator.Groups[modelID]
		if group != nil {
			for _, participant := range group.Members {
				if calculator.ConsensusWeights[participant] > 0 {
					intentHostCount++
				}
				intentWeight += calculator.ConsensusWeights[participant]
			}
		}

		meetsWeightThreshold := calculator.MeetsWeightThreshold(modelID)
		meetsVMin := calculator.MeetsMinHosts(modelID)
		meetsReachability := calculator.MeetsReachabilityThreshold(modelID)
		results = append(results, &types.BootstrapModelPreEligibility{
			ModelId:              modelID,
			PreEligible:          calculator.IsGroupPreEligible(modelID) && meetsReachability,
			MeetsWeightThreshold: meetsWeightThreshold,
			MeetsVMin:            meetsVMin,
			MeetsReachability:    meetsReachability,
			IntentHostCount:      intentHostCount,
			IntentWeight:         intentWeight,
			ReachableVotingPower: calculator.ProjectedReachableVotingPower(modelID),
		})
	}
	return results
}

func indexBootstrapPreEligibility(
	results []*types.BootstrapModelPreEligibility,
) map[string]*types.BootstrapModelPreEligibility {
	resultByModel := make(map[string]*types.BootstrapModelPreEligibility, len(results))
	for _, result := range results {
		if result == nil || result.ModelId == "" {
			continue
		}
		resultByModel[result.ModelId] = result
	}
	return resultByModel
}

func (am AppModule) emitBootstrapPreEligibilityEvents(
	ctx context.Context,
	snapshot types.BootstrapDelegationSnapshot,
) {
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	for _, result := range snapshot.Preeligibility {
		sdkCtx.EventManager().EmitEvent(sdk.NewEvent(
			"bootstrap_model_preeligibility",
			sdk.NewAttribute("snapshot_height", strconv.FormatInt(snapshot.SnapshotHeight, 10)),
			sdk.NewAttribute("model_id", result.ModelId),
			sdk.NewAttribute("pre_eligible", strconv.FormatBool(result.PreEligible)),
			sdk.NewAttribute("meets_weight_threshold", strconv.FormatBool(result.MeetsWeightThreshold)),
			sdk.NewAttribute("meets_v_min", strconv.FormatBool(result.MeetsVMin)),
			sdk.NewAttribute("meets_reachability", strconv.FormatBool(result.MeetsReachability)),
			sdk.NewAttribute("intent_host_count", strconv.FormatInt(result.IntentHostCount, 10)),
			sdk.NewAttribute("intent_weight", strconv.FormatInt(result.IntentWeight, 10)),
			sdk.NewAttribute("reachable_voting_power", strconv.FormatInt(result.ReachableVotingPower, 10)),
			sdk.NewAttribute("total_network_weight", strconv.FormatInt(snapshot.TotalNetworkWeight, 10)),
		))
	}
}

// ComputeModelVotingPowers computes per-model voting powers for PoC validation acceptance.
// DIRECT membership comes from store commit keys (participants who submitted PoC).
// Delegation-resolved: each DIRECT member's votingPower includes delegated consensus weight.
// Uses AP(N) consensus weights as the base.
func ComputeModelVotingPowers(
	storeCommitKeys []types.PoCParticipantModelKey,
	consensusWeights map[string]int64,
	delegations map[string]map[string]string,
) map[string]map[string]int64 {
	directMembers := make(map[string]map[string]bool)
	for _, key := range storeCommitKeys {
		if directMembers[key.ModelID] == nil {
			directMembers[key.ModelID] = make(map[string]bool)
		}
		directMembers[key.ModelID][key.ParticipantAddress] = true
	}

	modelVotingPowers := make(map[string]map[string]int64, len(directMembers))

	for _, modelID := range sortedKeys(directMembers) {
		members := directMembers[modelID]
		vp := make(map[string]int64, len(members))

		for _, addr := range sortedKeys(members) {
			vp[addr] = consensusWeights[addr]
		}

		// Add delegated weight
		modelDelegations := delegations[modelID]
		for _, delegator := range sortedKeys(modelDelegations) {
			target := modelDelegations[delegator]
			if !members[target] {
				continue
			}
			if members[delegator] {
				continue
			}
			vp[target] += consensusWeights[delegator]
		}

		modelVotingPowers[modelID] = vp
	}

	return modelVotingPowers
}

// VotingPowerCapParams carries the optional per-model concentration cap
// applied to per-model voting powers after delegation is resolved.
//
// PerModel is the max fraction of voting power any single host can hold
// within a single model group. Applied independently to each model.
// Zero means disabled.
//
// The per-model cap protects validation integrity: in slot-based validation,
// a host that attracts enough delegations could single-handedly push a model
// group past its supermajority threshold.
type VotingPowerCapParams struct {
	PerModel mathsdk.LegacyDec
}

// delegationVotingPowerCapParams extracts the voting-power cap from
// governance params. Returns zero-dec if not set, which disables the cap.
func (am AppModule) delegationVotingPowerCapParams(params types.Params) VotingPowerCapParams {
	if params.DelegationParams == nil {
		return VotingPowerCapParams{PerModel: mathsdk.LegacyZeroDec()}
	}
	return VotingPowerCapParams{
		PerModel: protoDecToLegacy(params.DelegationParams.MaxModelVotingPowerPercentage),
	}
}

// computeAndSetVotingPowers computes per-group voting powers from final weights
// and writes them to each participant's VotingPowers field for visibility.
//
// Applies the per-model concentration cap (if configured). Any host whose
// voting power exceeds the cap is clipped down to the cap and the excess is
// burned (not redistributed). The group's post-cap total shrinks
// accordingly; downstream per-group math is expected to operate on the
// post-cap total. See capPerModelVotingPowers for the rationale.
func (am AppModule) computeAndSetVotingPowers(
	activeParticipants []*types.ActiveParticipant,
	dwc *DelegationWeightCalculator,
	eligibleModels []string,
	participationByModel map[string]map[string]ParticipationMode,
	caps VotingPowerCapParams,
) {
	finalWeights := make(map[string]int64, len(activeParticipants))
	for _, p := range activeParticipants {
		finalWeights[p.Index] = p.Weight
	}

	participantVP := make(map[string][]*types.ModelVotingPower)

	for _, modelID := range eligibleModels {
		modes := participationByModel[modelID]
		if modes == nil {
			continue
		}
		vpMap := dwc.ComputeGroupVotingPowers(modelID, modes, finalWeights)

		// Per-model cap: scale down any single host that exceeds the cap.
		if !caps.PerModel.IsZero() {
			capPerModelVotingPowers(vpMap, caps.PerModel, modelID, am)
		}

		for _, addr := range sortedKeys(vpMap) {
			vp := vpMap[addr]
			if vp > 0 {
				participantVP[addr] = append(participantVP[addr], &types.ModelVotingPower{
					ModelId:     modelID,
					VotingPower: vp,
				})
			}
		}
	}

	for _, p := range activeParticipants {
		vps := participantVP[p.Index]
		if len(vps) == 0 {
			continue
		}
		slices.SortFunc(vps, func(a, b *types.ModelVotingPower) int {
			return cmp.Compare(a.ModelId, b.ModelId)
		})
		p.VotingPowers = vps
	}
}

// votingPowerCapLogger is a minimal interface for the cap helpers' logging.
// The real production type (AppModule) satisfies it; tests can pass a nop
// implementation or capture logs directly.
type votingPowerCapLogger interface {
	LogInfo(msg string, subSystem types.SubSystem, keyvals ...interface{})
	LogWarn(msg string, subSystem types.SubSystem, keyvals ...interface{})
}

// sumInt64Safe sums the values of a string-keyed int64 map, returning
// (sum, true) on success or (0, false) if the sum would overflow int64.
// Iterates over sorted keys so the accumulation order stays deterministic,
// which keeps any future log/event added inside the loop identical across
// nodes.
func sumInt64Safe(m map[string]int64) (int64, bool) {
	var total int64
	for _, k := range sortedKeys(m) {
		v := m[k]
		if v < 0 {
			return 0, false
		}
		sum, carry := bits.Add64(uint64(total), uint64(v), 0)
		if carry != 0 || sum > uint64(math.MaxInt64) {
			return 0, false
		}
		total = int64(sum)
	}
	return total, true
}

// capPerModelVotingPowers clips any host whose voting power in vpMap exceeds
// capPct of the ORIGINAL group total down to the cap. The excess is burned:
// it is not redistributed to other hosts in the group. Applied in-place.
//
// Why burn rather than redistribute: voting power that reached a host via
// delegation represents the delegator's explicit trust in THAT host. Moving
// the excess to other hosts in the group would silently reassign that trust
// to parties the delegator didn't pick. Burning is the only option that
// respects the delegator's choice without requiring per-delegation
// accounting through the cap path.
//
// The cap is applied against the original pre-capping total, so the cap
// value is stable regardless of how many hosts end up clipped. The group's
// post-cap total shrinks, and downstream per-group math (2/3 validation
// quorum, per-group reward shares, slot sampling) is expected to operate on
// the post-cap total. Consensus-weight concentration is capped separately
// and is unaffected by this function.
//
// Complexity: O(N) single pass over vpMap.
func capPerModelVotingPowers(vpMap map[string]int64, capPct mathsdk.LegacyDec, modelID string, logger votingPowerCapLogger) {
	if len(vpMap) < 2 {
		return
	}

	totalVP, ok := sumInt64Safe(vpMap)
	if !ok {
		logger.LogWarn("per-model voting power cap: total VP overflow, cap skipped",
			types.EpochGroup,
			"modelId", modelID,
		)
		return
	}
	if totalVP == 0 {
		return
	}

	capVP := capPct.MulInt64(totalVP).TruncateInt64()
	if capVP <= 0 {
		return
	}

	// Iterate in sorted-address order so logged events are deterministic
	// across nodes. The order doesn't affect the outcome since each host is
	// clipped independently.
	for _, addr := range sortedKeys(vpMap) {
		vp := vpMap[addr]
		if vp <= capVP {
			continue
		}
		excess := vp - capVP
		vpMap[addr] = capVP
		logger.LogInfo("per-model voting power cap applied",
			types.EpochGroup,
			"modelId", modelID,
			"cappedHost", addr,
			"originalVP", vp,
			"capVP", capVP,
			"burned", excess,
		)
	}
}

// delegationAdjustmentParams extracts DelegationAdjustmentParams from governance params.
func (am AppModule) delegationAdjustmentParams(params types.Params) DelegationAdjustmentParams {
	if params.DelegationParams == nil {
		return DelegationAdjustmentParams{
			RefusalPenalty:         mathsdk.LegacyZeroDec(),
			NoParticipationPenalty: mathsdk.LegacyZeroDec(),
			DelegationShare:        mathsdk.LegacyZeroDec(),
		}
	}
	dp := params.DelegationParams
	return DelegationAdjustmentParams{
		RefusalPenalty:         protoDecToLegacy(dp.RefusalPenalty),
		NoParticipationPenalty: protoDecToLegacy(dp.NoParticipationPenalty),
		DelegationShare:        protoDecToLegacy(dp.DelegationShare),
	}
}

type weightSummaryLogger interface {
	LogInfo(msg string, subSystem types.SubSystem, keyvals ...interface{})
}

func formatModes(addr string, eligibleModels []string, modes map[string]map[string]ParticipationMode) string {
	parts := make([]string, 0, len(eligibleModels))
	for _, modelID := range eligibleModels {
		mode, ok := modes[modelID][addr]
		if !ok {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s:%s", modelID, mode))
	}
	return strings.Join(parts, ",")
}

func formatVotingPowers(vps []*types.ModelVotingPower) string {
	parts := make([]string, 0, len(vps))
	for _, vp := range vps {
		parts = append(parts, fmt.Sprintf("%s:%d", vp.ModelId, vp.VotingPower))
	}
	return strings.Join(parts, ",")
}

// emitWeightPipelineLogs prints one line per group and one line per
// participant so any final weight can be reproduced from the log alone.
// The gap between after_penalty and final is the combined effect of
// collateral adjustment and power-cap (both rarely active).
func emitWeightPipelineLogs(
	logger weightSummaryLogger,
	epoch uint64,
	groups []GroupSummary,
	eligibleModels []string,
	participants []*types.ActiveParticipant,
	modes map[string]map[string]ParticipationMode,
	consensus, afterPenalty map[string]int64,
	acc *PenaltyAccumulator,
) {
	for _, g := range groups {
		logger.LogInfo("weight_group", types.PoC,
			"epoch", epoch,
			"model", g.ModelID,
			"coeff", g.Coeff.String(),
			"raw_total", g.RawTotal,
			"cap", g.Cap,
			"scale", g.Scale.String(),
		)
	}

	sorted := make([]*types.ActiveParticipant, len(participants))
	copy(sorted, participants)
	slices.SortFunc(sorted, func(a, b *types.ActiveParticipant) int {
		return cmp.Compare(a.Index, b.Index)
	})

	for _, p := range sorted {
		logger.LogInfo("weight_pipeline", types.PoC,
			"epoch", epoch,
			"addr", p.Index,
			"modes", formatModes(p.Index, eligibleModels, modes),
			"consensus", consensus[p.Index],
			"penalty", acc.AppliedFraction(p.Index).String(),
			"transfer_in", acc.TransferIn(p.Index),
			"transfer_out", acc.TransferOut(p.Index),
			"after_penalty", afterPenalty[p.Index],
			"final", p.Weight,
			"vp", formatVotingPowers(p.VotingPowers),
		)
	}
}

func modelPenaltyStartEpochs(pocParams *types.PocParams) map[string]uint64 {
	if pocParams == nil {
		return map[string]uint64{}
	}
	result := make(map[string]uint64)
	for _, modelConfig := range pocParams.GetModelConfigs() {
		if modelConfig == nil || modelConfig.ModelId == "" {
			continue
		}
		result[modelConfig.ModelId] = modelConfig.PenaltyStartEpoch
	}
	return result
}
