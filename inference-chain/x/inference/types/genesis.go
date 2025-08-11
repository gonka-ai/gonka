package types

// DefaultIndex is the default global index
const DefaultIndex uint64 = 1

// DefaultGenesis returns the default genesis state
func GenerateGenesis(mockContracts bool) *GenesisState {
	// Do not embed CW20 wasm bytes or code id in genesis anymore.
	// Governance or app upgrades should populate code IDs post-genesis.
	contractsParams := CosmWasmParams{
		Cw20Code:   []byte{},
		Cw20CodeId: 0,
	}

	return &GenesisState{
		// this line is used by starport scaffolding # genesis/types/default
		Params:            DefaultParams(),
		GenesisOnlyParams: DefaultGenesisOnlyParams(),
		CosmWasmParams:    contractsParams,
		Bridge: &Bridge{
			ContractAddresses:   []*BridgeContractAddress{},
			TokenMetadata:       []*BridgeTokenMetadata{},
			TradeApprovedTokens: []*BridgeTokenReference{},
		},
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
