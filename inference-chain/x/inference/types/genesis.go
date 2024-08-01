package types

import (
	"fmt"
)

// DefaultIndex is the default global index
const DefaultIndex uint64 = 1

// DefaultGenesis returns the default genesis state
func DefaultGenesis() *GenesisState {
	return &GenesisState{
		InferenceList:   []Inference{},
		ParticipantList: []Participant{},
		// this line is used by starport scaffolding # genesis/types/default
		Params: DefaultParams(),
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
	// this line is used by starport scaffolding # genesis/types/validate

	return gs.Params.Validate()
}
