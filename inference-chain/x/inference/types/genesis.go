package types

import (
	"fmt"
)

// DefaultIndex is the default global index
const DefaultIndex uint64 = 1

// DefaultGenesis returns the default genesis state
func DefaultGenesis() *GenesisState {
	return &GenesisState{
		InferenceList:                  []Inference{},
		ParticipantList:                []Participant{},
		EpochGroupDataList:             []EpochGroupData{},
		SettleAmountList:               []SettleAmount{},
		EpochGroupValidationsList:      []EpochGroupValidations{},
		TokenomicsData:                 &TokenomicsData{},
		TopMinerList:                   []TopMiner{},
		InferenceTimeoutList:           []InferenceTimeout{},
		InferenceValidationDetailsList: []InferenceValidationDetails{},
		EpochPerformanceSummaryList:    []EpochPerformanceSummary{},
		// this line is used by starport scaffolding # genesis/types/default
		Params:            DefaultParams(),
		GenesisOnlyParams: DefaultGenesisOnlyParams(),
	}
}

// Validate performs basic genesis state validation returning an error upon any
// failure.
func (gs GenesisState) Validate() error {
	// Check for duplicated index in inference
	inferenceIndexMap := make(map[string]struct{})

	for _, elem := range gs.InferenceList {
		index := string(InferenceKey(elem.Index))
		if _, ok := inferenceIndexMap[index]; ok {
			return fmt.Errorf("duplicated index for inference")
		}
		inferenceIndexMap[index] = struct{}{}
	}
	// Check for duplicated index in participant
	participantIndexMap := make(map[string]struct{})

	for _, elem := range gs.ParticipantList {
		index := string(ParticipantKey(elem.Index))
		if _, ok := participantIndexMap[index]; ok {
			return fmt.Errorf("duplicated index for participant")
		}
		participantIndexMap[index] = struct{}{}
	}
	// Check for duplicated index in epochGroupData
	epochGroupDataIndexMap := make(map[string]struct{})

	for _, elem := range gs.EpochGroupDataList {
		index := string(EpochGroupDataKey(elem.PocStartBlockHeight))
		if _, ok := epochGroupDataIndexMap[index]; ok {
			return fmt.Errorf("duplicated index for epochGroupData")
		}
		epochGroupDataIndexMap[index] = struct{}{}
	}
	// Check for duplicated index in settleAmount
	settleAmountIndexMap := make(map[string]struct{})

	for _, elem := range gs.SettleAmountList {
		index := string(SettleAmountKey(elem.Participant))
		if _, ok := settleAmountIndexMap[index]; ok {
			return fmt.Errorf("duplicated index for settleAmount")
		}
		settleAmountIndexMap[index] = struct{}{}
	}
	// Check for duplicated index in epochGroupValidations
	epochGroupValidationsIndexMap := make(map[string]struct{})

	for _, elem := range gs.EpochGroupValidationsList {
		index := string(EpochGroupValidationsKey(elem.Participant, elem.PocStartBlockHeight))
		if _, ok := epochGroupValidationsIndexMap[index]; ok {
			return fmt.Errorf("duplicated index for epochGroupValidations")
		}
		epochGroupValidationsIndexMap[index] = struct{}{}
	}
	// Check for duplicated index in topMiner
	topMinerIndexMap := make(map[string]struct{})

	for _, elem := range gs.TopMinerList {
		index := string(TopMinerKey(elem.Address))
		if _, ok := topMinerIndexMap[index]; ok {
			return fmt.Errorf("duplicated index for topMiner")
		}
		topMinerIndexMap[index] = struct{}{}
	}
	// Check for duplicated index in inferenceTimeout
	inferenceTimeoutIndexMap := make(map[string]struct{})

	for _, elem := range gs.InferenceTimeoutList {
		index := string(InferenceTimeoutKey(elem.ExpirationHeight, elem.InferenceId))
		if _, ok := inferenceTimeoutIndexMap[index]; ok {
			return fmt.Errorf("duplicated index for inferenceTimeout")
		}
		inferenceTimeoutIndexMap[index] = struct{}{}
	}
	// Check for duplicated index in inferenceValidationDetails
	inferenceValidationDetailsIndexMap := make(map[string]struct{})

	for _, elem := range gs.InferenceValidationDetailsList {
		index := string(InferenceValidationDetailsKey(elem.EpochId, elem.InferenceId))
		if _, ok := inferenceValidationDetailsIndexMap[index]; ok {
			return fmt.Errorf("duplicated index for inferenceValidationDetails")
		}
		inferenceValidationDetailsIndexMap[index] = struct{}{}
	}
	// Check for duplicated index in epochPerformanceSummary
	epochPerformanceSummaryIndexMap := make(map[string]struct{})

	for _, elem := range gs.EpochPerformanceSummaryList {
		index := string(EpochPerformanceSummaryKey(elem.EpochStartHeight, elem.ParticipantId))
		if _, ok := epochPerformanceSummaryIndexMap[index]; ok {
			return fmt.Errorf("duplicated index for epochPerformanceSummary")
		}
		epochPerformanceSummaryIndexMap[index] = struct{}{}
	}
	// this line is used by starport scaffolding # genesis/types/validate

	return gs.Params.Validate()
}
