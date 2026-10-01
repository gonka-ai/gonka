package inference

import (
	"context"
	"slices"

	"github.com/productscience/inference/x/inference/types"
)

// BootstrapPenaltyMode is bootstrap-specific and intentionally separate from the
// shared next-epoch ParticipationMode used by DelegationWeightCalculator.
type BootstrapPenaltyMode int

const (
	BootstrapPenaltyDirect BootstrapPenaltyMode = iota
	BootstrapPenaltyDelegate
	BootstrapPenaltyIntentMissed
	BootstrapPenaltyNone
)

type bootstrapPenaltyInputs struct {
	Delegations   map[string]map[string]string
	Intents       map[string]map[string]bool
	ReportByModel map[string]*types.BootstrapModelPreEligibility
}

func (i bootstrapPenaltyInputs) modelSet() map[string]bool {
	result := make(map[string]bool, len(i.ReportByModel))
	for _, modelID := range sortedKeys(i.ReportByModel) {
		result[modelID] = true
	}
	return result
}

func (am AppModule) loadBootstrapPenaltyInputs(ctx context.Context) (bootstrapPenaltyInputs, bool) {
	snapshot, delegations, intents, found := am.loadBootstrapSnapshotState(ctx)
	if !found {
		return bootstrapPenaltyInputs{}, false
	}

	return bootstrapPenaltyInputs{
		Delegations:   delegations,
		Intents:       intents,
		ReportByModel: indexBootstrapPreEligibility(snapshot.Preeligibility),
	}, true
}

func (am AppModule) loadBootstrapDirectCommitters(
	ctx context.Context,
	pocStageStartHeight int64,
	modelSet map[string]bool,
) (map[string]map[string]bool, error) {
	allStoreCommits, err := am.keeper.GetAllPoCV2StoreCommitsForStage(ctx, pocStageStartHeight)
	if err != nil {
		return nil, err
	}
	validations, err := am.keeper.GetPoCValidationsV2ByStage(ctx, pocStageStartHeight)
	if err != nil {
		return nil, err
	}

	return validatedDirectCommitters(allStoreCommits, validations, modelSet), nil
}

// validatedDirectCommitters builds the (model -> participant) direct-committer
// set from raw PoC store commits, keeping only commits that entered the
// validation process: at least one validator vote must exist for the
// (participant, model) pair.
//
// Rationale: a store commit alone proves nothing about real inference work —
// validateNewCommit enforces only syntax (count > 0, 32-byte root, depth
// 1..32), so a syntactically valid but meaningless commit used to earn the
// BootstrapPenaltyDirect exemption and dodge the NoParticipationPenalty at the
// cost of a single transaction fee (B-1). Requiring validation entry mirrors
// the "no validations" gate in validatedParticipant (chainvalidation.go): the
// commit must have been seen by the validation pipeline. This preserves the
// design intent for genuinely failed launches (PR #1740): raw commits remain
// the signal (no validated weight is required), but commits nobody ever
// validated no longer count as direct participation. Commits filtered out here
// fall through to the delegation/intent checks in ResolveBootstrapPenaltyModes
// instead of being force-demoted, so genuine delegation signals are preserved.
func validatedDirectCommitters(
	allStoreCommits map[types.PoCParticipantModelKey]types.PoCV2StoreCommit,
	validations map[types.PoCParticipantModelKey][]types.PoCValidationV2,
	modelSet map[string]bool,
) map[string]map[string]bool {
	storeCommitKeys := sortedStoreCommitKeys(allStoreCommits)
	directCommitters := make(map[string]map[string]bool)
	for _, key := range storeCommitKeys {
		if !modelSet[key.ModelID] {
			continue
		}
		if len(validations[key]) == 0 {
			continue
		}
		if directCommitters[key.ModelID] == nil {
			directCommitters[key.ModelID] = make(map[string]bool)
		}
		directCommitters[key.ModelID][key.ParticipantAddress] = true
	}
	return directCommitters
}

func (am AppModule) resolveBootstrapPenaltyModes(
	ctx context.Context,
	participants []*types.ActiveParticipant,
	pocStageStartHeight int64,
	inputs bootstrapPenaltyInputs,
	previous *previousConfirmedWeights,
) (map[string]map[string]BootstrapPenaltyMode, error) {
	if len(inputs.ReportByModel) == 0 {
		return map[string]map[string]BootstrapPenaltyMode{}, nil
	}

	directCommitters, err := am.loadBootstrapDirectCommitters(ctx, pocStageStartHeight, inputs.modelSet())
	if err != nil {
		return nil, err
	}

	return ResolveBootstrapPenaltyModes(
		participants,
		previousRootMembers(previous),
		inputs.ReportByModel,
		inputs.Delegations,
		inputs.Intents,
		directCommitters,
	), nil
}

func previousRootMembers(previous *previousConfirmedWeights) map[string]bool {
	members := make(map[string]bool)
	if previous == nil {
		return members
	}
	for addr := range previous.weights {
		members[addr] = true
	}
	return members
}

func ResolveBootstrapPenaltyModes(
	participants []*types.ActiveParticipant,
	previousRoot map[string]bool,
	reportByModel map[string]*types.BootstrapModelPreEligibility,
	delegations map[string]map[string]string,
	intents map[string]map[string]bool,
	directCommitters map[string]map[string]bool,
) map[string]map[string]BootstrapPenaltyMode {
	modelIDs := make([]string, 0, len(reportByModel))
	for modelID := range reportByModel {
		modelIDs = append(modelIDs, modelID)
	}
	slices.Sort(modelIDs)

	modes := make(map[string]map[string]BootstrapPenaltyMode, len(modelIDs))
	for _, modelID := range modelIDs {
		report := reportByModel[modelID]
		if report == nil || !report.PreEligible {
			continue
		}

		modelModes := make(map[string]BootstrapPenaltyMode)
		modelDelegations := delegations[modelID]
		modelIntents := intents[modelID]
		modelCommitters := directCommitters[modelID]

		for _, participant := range participants {
			if participant == nil || participant.Weight <= 0 {
				continue
			}

			addr := participant.Index
			if !previousRoot[addr] {
				continue
			}
			switch {
			case modelCommitters[addr]:
				modelModes[addr] = BootstrapPenaltyDirect
			case modelDelegations != nil && modelDelegations[addr] != "":
				modelModes[addr] = BootstrapPenaltyDelegate
			case modelIntents != nil && modelIntents[addr]:
				modelModes[addr] = BootstrapPenaltyIntentMissed
			default:
				modelModes[addr] = BootstrapPenaltyNone
			}
		}

		modes[modelID] = modelModes
	}

	return modes
}

// AccumulateBootstrapPenalties adds penalty fractions for non-eligible bootstrap
// models into the accumulator. Eligible models are fully handled by regular
// delegation adjustment and skipped here.
func AccumulateBootstrapPenalties(
	acc *PenaltyAccumulator,
	modes map[string]map[string]BootstrapPenaltyMode,
	eligibleModels []string,
	params DelegationAdjustmentParams,
	upcomingEpochIndex uint64,
	penaltyStartEpochByModel map[string]uint64,
) {
	if params.IsNoOp() || len(modes) == 0 {
		return
	}

	eligibleSet := make(map[string]bool, len(eligibleModels))
	for _, modelID := range eligibleModels {
		eligibleSet[modelID] = true
	}

	modelIDs := make([]string, 0, len(modes))
	for modelID := range modes {
		modelIDs = append(modelIDs, modelID)
	}
	slices.Sort(modelIDs)

	for _, modelID := range modelIDs {
		if eligibleSet[modelID] {
			continue
		}
		if !penaltyStartReached(modelID, upcomingEpochIndex, penaltyStartEpochByModel) {
			continue
		}

		for _, addr := range sortedKeys(modes[modelID]) {
			mode := modes[modelID][addr]
			if acc.originalWeight[addr] <= 0 {
				continue
			}

			switch mode {
			case BootstrapPenaltyIntentMissed, BootstrapPenaltyNone:
				if !params.NoParticipationPenalty.IsZero() {
					acc.AddPenalty(addr, params.NoParticipationPenalty)
				}
			}
		}
	}
}
