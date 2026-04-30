package types

// this line is used by starport scaffolding # genesis/types/import

import "fmt"

// DefaultIndex is the default global index
const DefaultIndex uint64 = 1

// DefaultGenesis returns the default genesis state
func DefaultGenesis() *GenesisState {
	return &GenesisState{
		// this line is used by starport scaffolding # genesis/types/default
		Params:                DefaultParams(),
		ActiveEpochId:         0, // No active DKG by default
		CurrentSigningEpochId: 0,
		BlsDataList:           []EpochBLSData{},
		SigningRequests:       []ThresholdSigningRequest{},
		GroupValidationStates: []GroupKeyValidationState{},
	}
}

// Validate performs basic genesis state validation returning an error upon any
// failure.
func (gs GenesisState) Validate() error {
	// this line is used by starport scaffolding # genesis/types/validate

	// Check for duplicated EpochId in BlsDataList
	epochIdMap := make(map[uint64]bool)
	for _, data := range gs.BlsDataList {
		if epochIdMap[data.EpochId] {
			return fmt.Errorf("duplicated epoch id in bls data list: %d", data.EpochId)
		}
		epochIdMap[data.EpochId] = true
	}

	// Check for duplicated RequestId in SigningRequests
	requestIdMap := make(map[string]bool)
	for _, req := range gs.SigningRequests {
		reqIdStr := string(req.RequestId)
		if requestIdMap[reqIdStr] {
			return fmt.Errorf("duplicated request id in signing requests")
		}
		requestIdMap[reqIdStr] = true
	}

	// Check for duplicated NewEpochId in GroupValidationStates
	groupValidationMap := make(map[uint64]bool)
	for _, state := range gs.GroupValidationStates {
		if groupValidationMap[state.NewEpochId] {
			return fmt.Errorf("duplicated new epoch id in group validation states: %d", state.NewEpochId)
		}
		groupValidationMap[state.NewEpochId] = true
	}

	return gs.Params.Validate()
}
