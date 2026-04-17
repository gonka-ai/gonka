package inference

import (
	"cmp"
	"context"
	"math"
	"math/big"
	"math/bits"
	"slices"
	"strconv"

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

	return &DelegationWeightCalculator{
		Groups:               groups,
		ConsensusWeights:     consensusWeights,
		TotalNetworkWeight:   totalWeight,
		Delegations:          nextEpochDelegations,
		Refusals:             nextEpochRefusals,
		Params:               buildWeightParams(params),
		PrevMemberPocWeights: prevState.perModelPocWeights,
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
	coefficients := ModelCoefficients(params.PocParams)
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
				g.MemberPocWeights[p.Index] = SumNodeWeights(p.MlNodes[i].MlNodes)
			} else if logger != nil {
				logger.LogWarn("buildGroupData: Models/MlNodes parallel array mismatch", types.PoC,
					"participant", p.Index, "modelIndex", i, "modelsLen", len(p.Models), "mlNodesLen", len(p.MlNodes))
			}
		}
	}

	return groups
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
func (am AppModule) captureDelegationSnapshot(ctx context.Context, blockHeight int64) {
	snapshot, err := am.buildDelegationSnapshot(ctx, blockHeight)
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

func (am AppModule) buildDelegationSnapshot(ctx context.Context, blockHeight int64) (types.DelegationSnapshot, error) {
	params, err := am.keeper.GetParams(ctx)
	if err != nil {
		return types.DelegationSnapshot{}, err
	}

	effectiveState := am.getEffectiveValidationBaseState(ctx)
	effectiveParticipants := effectiveState.participants
	modelIDs := approvedModelIDs(params.PocParams)
	delegationEntries, refusalEntries := am.loadFilteredDelegationSnapshotState(ctx, effectiveParticipants, modelIDs)

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
	effectiveParticipants []*types.ActiveParticipant,
	modelIDs []string,
) ([]*types.PoCDelegation, []*types.PoCRefusal) {
	delegationEntries := make([]*types.PoCDelegation, 0)
	refusalEntries := make([]*types.PoCRefusal, 0)

	for _, participant := range effectiveParticipants {
		for _, modelID := range modelIDs {
			delegation, found := am.keeper.GetPoCDelegation(ctx, modelID, participant.Index)
			if found {
				delegationCopy := delegation
				delegationEntries = append(delegationEntries, &delegationCopy)
			}

			if am.keeper.HasPoCRefusal(ctx, modelID, participant.Index) {
				refusalEntries = append(refusalEntries, &types.PoCRefusal{
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
	coefficients := ModelCoefficients(params.PocParams)
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
// Applies the per-model concentration cap (if configured). The cap uses a
// proportional redistribution algorithm: any excess over the cap is removed
// from the capped host, and the remaining uncapped hosts absorb it in
// proportion to their existing share. Hosts that are themselves at or above
// the cap are skipped during redistribution so excess cannot be routed back
// to them.
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
		filtered := vps[:0]
		for _, mvp := range vps {
			if mvp.VotingPower > 0 {
				filtered = append(filtered, mvp)
			}
		}
		if len(filtered) == 0 {
			continue
		}
		slices.SortFunc(filtered, func(a, b *types.ModelVotingPower) int {
			return cmp.Compare(a.ModelId, b.ModelId)
		})
		p.VotingPowers = filtered
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

// mulDivInt64 computes floor(a*b/c) using math/big to avoid int64 overflow
// in the intermediate a*b product. Returns (0, false) if any input is
// negative, if c is zero, or if the result would exceed int64. Callers
// should treat a false return as "skip this redistribution step" — the
// existing cap helpers guarantee result <= min(a, b) when inputs are
// well-formed, so a false return indicates a caller bug or pathological
// governance parameters.
func mulDivInt64(a, b, c int64) (int64, bool) {
	if a < 0 || b < 0 || c <= 0 {
		return 0, false
	}
	num := new(big.Int).Mul(big.NewInt(a), big.NewInt(b))
	num.Quo(num, big.NewInt(c))
	if !num.IsInt64() {
		return 0, false
	}
	return num.Int64(), true
}

// capPerModelVotingPowers scales down any host whose voting power in vpMap
// exceeds capPct of the group total, redistributing the excess proportionally
// to the remaining hosts that are below the cap. Applied in-place.
//
// Algorithm: single-pass after one sort. We walk hosts in descending voting
// power order, capping each host whose VP exceeds the running cap. The cap
// is computed against the ORIGINAL total (not the iteratively-recomputed
// total) because the cap is a protocol threshold and should not depend on
// how many previous hosts were capped. Excess from each capped host is
// distributed proportionally to all hosts that remain below the cap.
//
// Complexity: O(N log N) sort + O(N) passes. Total O(N log N).
//
// Overflow safety: intermediate products use math/big to avoid int64
// overflow on large voting power values.
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

	// Sort hosts once, descending by VP (stable on addr for determinism).
	keys := sortedKeys(vpMap) // ascending by addr
	slices.SortStableFunc(keys, func(a, b string) int {
		return cmp.Compare(vpMap[b], vpMap[a])
	})

	// First pass: cap anyone over the cap and record total excess.
	//
	// Design note: the cap is applied against the ORIGINAL pre-capping total.
	// After redistribution, recipients can end up above capVP if the group
	// is small and the capped host's excess is large relative to recipient
	// capacity. This is an intentional trade-off: total voting power is
	// conserved (no burn) at the cost of strict per-host enforcement on
	// tiny groups. Alternatives (iterate to convergence, burn excess) were
	// considered and rejected — iteration diverged non-trivially under
	// rounding and burning changed total VP mid-pipeline, which has
	// knock-on effects in downstream reward and slot-sampling calculations.
	totalExcess := int64(0)
	for _, addr := range keys {
		vp := vpMap[addr]
		if vp <= capVP {
			break // hosts are sorted descending; no more over-cap hosts
		}
		excess := vp - capVP
		// Guard against int64 overflow when accumulating excess.
		sum, carry := bits.Add64(uint64(totalExcess), uint64(excess), 0)
		if carry != 0 || sum > uint64(math.MaxInt64) {
			logger.LogWarn("per-model voting power cap: excess overflow, cap truncated",
				types.EpochGroup,
				"modelId", modelID,
			)
			totalExcess = math.MaxInt64
			vpMap[addr] = capVP
			break
		}
		totalExcess = int64(sum)
		vpMap[addr] = capVP
		logger.LogInfo("per-model voting power cap applied",
			types.EpochGroup,
			"modelId", modelID,
			"cappedHost", addr,
			"originalVP", vp,
			"capVP", capVP,
			"excess", excess,
		)
	}
	if totalExcess == 0 {
		return
	}

	// Second pass: collect recipients strictly below the cap, sum their VP.
	recipientTotal := int64(0)
	recipientKeys := make([]string, 0, len(keys))
	for _, addr := range keys {
		if vpMap[addr] < capVP {
			recipientKeys = append(recipientKeys, addr)
			// No overflow possible: all vpMap values are part of totalVP which
			// we've already summed safely above.
			recipientTotal += vpMap[addr]
		}
	}
	// Stable order for deterministic distribution — re-sort by addr for
	// predictable rounding-remainder assignment.
	slices.Sort(recipientKeys)

	if recipientTotal == 0 {
		logger.LogWarn("per-model voting power cap: no redistribution room, excess dropped",
			types.EpochGroup,
			"modelId", modelID,
			"excess", totalExcess,
		)
		return
	}

	// Third pass: distribute excess proportionally using safe big-int math.
	// share = excess * recipientVP / recipientTotal.
	distributed := int64(0)
	for _, addr := range recipientKeys {
		share, ok := mulDivInt64(totalExcess, vpMap[addr], recipientTotal)
		if !ok {
			logger.LogWarn("per-model voting power cap: share overflow, skipping recipient",
				types.EpochGroup,
				"modelId", modelID,
				"recipient", addr,
			)
			continue
		}
		vpMap[addr] += share
		distributed += share
	}
	// Rounding remainder: give to the first recipient (deterministic).
	if remainder := totalExcess - distributed; remainder > 0 && len(recipientKeys) > 0 {
		vpMap[recipientKeys[0]] += remainder
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
