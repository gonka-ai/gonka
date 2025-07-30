package types

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
	// this line is used by starport scaffolding # genesis/types/validate

	return gs.Params.Validate()
}
