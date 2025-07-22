package types

import (
	"fmt"
)

// DefaultIndex is the default global index
const DefaultIndex uint64 = 1

// DefaultGenesis returns the default genesis state
func GenerateGenesis(mockContracts bool) *GenesisState {
	var contractsParams CosmWasmParams
	if mockContracts {
		contractsParams = CosmWasmParams{
			Cw20Code:   []byte{},
			Cw20CodeId: 0,
		}
	} else {
		contractsParams = *GetDefaultCW20ContractsParams()
	}

	return &GenesisState{
		ParticipantList:    []Participant{},
		EpochGroupDataList: []EpochGroupData{},
		TokenomicsData:     &TokenomicsData{},
		TopMinerList:       []TopMiner{},
		PartialUpgradeList: []PartialUpgrade{},
		// this line is used by starport scaffolding # genesis/types/default
		Params:            DefaultParams(),
		GenesisOnlyParams: DefaultGenesisOnlyParams(),
		CosmWasmParams:    contractsParams,
	}
}

func MockedGenesis() *GenesisState {
	return GenerateGenesis(true)
}

func DefaultGenesis() *GenesisState {
	return GenerateGenesis(false)
}

// Validate performs basic genesis state validation returning an error upon any
// failure.
func (gs GenesisState) Validate() error {
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
		index := string(EpochGroupDataKey(elem.PocStartBlockHeight, elem.ModelId))
		if _, ok := epochGroupDataIndexMap[index]; ok {
			return fmt.Errorf("duplicated index for epochGroupData")
		}
		epochGroupDataIndexMap[index] = struct{}{}
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
	// Check for duplicated index in partialUpgrade
	partialUpgradeIndexMap := make(map[string]struct{})

	for _, elem := range gs.PartialUpgradeList {
		index := string(PartialUpgradeKey(elem.Height))
		if _, ok := partialUpgradeIndexMap[index]; ok {
			return fmt.Errorf("duplicated index for partialUpgrade")
		}
		partialUpgradeIndexMap[index] = struct{}{}
	}
	// this line is used by starport scaffolding # genesis/types/validate

	return gs.Params.Validate()
}
