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
	BootstrapPenaltyIntentOK
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

	storeCommitKeys := sortedStoreCommitKeys(allStoreCommits)
	directCommitters := make(map[string]map[string]bool)
	for _, key := range storeCommitKeys {
		if !modelSet[key.ModelID] {
			continue
		}
		if directCommitters[key.ModelID] == nil {
			directCommitters[key.ModelID] = make(map[string]bool)
		}
		directCommitters[key.ModelID][key.ParticipantAddress] = true
	}
	return directCommitters, nil
}

func (am AppModule) resolveBootstrapPenaltyModes(
	ctx context.Context,
	participants []*types.ActiveParticipant,
	pocStageStartHeight int64,
	inputs bootstrapPenaltyInputs,
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
		inputs.ReportByModel,
		inputs.Delegations,
		inputs.Intents,
		directCommitters,
	), nil
}

func ResolveBootstrapPenaltyModes(
	participants []*types.ActiveParticipant,
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
		preEligible := report != nil && report.PreEligible

		modelModes := make(map[string]BootstrapPenaltyMode)
		modelDelegations := delegations[modelID]
		modelIntents := intents[modelID]
		modelCommitters := directCommitters[modelID]

		for _, participant := range participants {
			if participant == nil || participant.Weight <= 0 {
				continue
			}

			addr := participant.Index
			intentMode := BootstrapPenaltyIntentOK
			if preEligible {
				intentMode = BootstrapPenaltyIntentMissed
			}
			switch {
			case modelCommitters[addr]:
				modelModes[addr] = BootstrapPenaltyDirect
			case modelDelegations != nil && modelDelegations[addr] != "":
				modelModes[addr] = BootstrapPenaltyDelegate
			case modelIntents != nil && modelIntents[addr]:
				modelModes[addr] = intentMode
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
