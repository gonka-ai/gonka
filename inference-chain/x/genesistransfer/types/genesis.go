package types

// this line is used by starport scaffolding # genesis/types/import

// DefaultIndex is the default global index
const DefaultIndex uint64 = 1

// DefaultGenesis returns the default genesis state
func DefaultGenesis() *GenesisState {
	return &GenesisState{
		// this line is used by starport scaffolding # genesis/types/default
		Params:          DefaultParams(),
		TransferRecords: []TransferRecord{},
	}
}

// Validate performs basic genesis state validation returning an error upon any
// failure.
func (gs GenesisState) Validate() error {
	// this line is used by starport scaffolding # genesis/types/validate

	// Validate transfer records
	seenAddresses := make(map[string]bool)
	for _, record := range gs.TransferRecords {
		if err := record.Validate(); err != nil {
			return err
		}

		// Check for duplicate transfer records
		if seenAddresses[record.GenesisAddress] {
			return ErrDuplicateTransferRecord
		}
		seenAddresses[record.GenesisAddress] = true
	}

	return gs.Params.Validate()
}
