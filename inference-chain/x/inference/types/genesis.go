package types

import "fmt"

// DefaultIndex is the default global index
const DefaultIndex uint64 = 1

// DefaultGenesis returns the default genesis state
func GenerateGenesis() *GenesisState {
	// Do not embed CW20 wasm bytes or code id in genesis anymore.
	// Governance or app upgrades should populate code IDs post-genesis.

	return &GenesisState{
		// this line is used by starport scaffolding # genesis/types/default
		Params:            DefaultParams(),
		GenesisOnlyParams: DefaultGenesisOnlyParams(),
		Bridge: &Bridge{
			ContractAddresses:   []*BridgeContractAddress{},
			TokenMetadata:       []*BridgeTokenMetadata{},
			TradeApprovedTokens: []*BridgeTokenReference{},
		},
	}
}

func DefaultGenesis() *GenesisState {
	return GenerateGenesis()
}

// Validate performs basic genesis state validation returning an error upon any
// failure.
func (gs GenesisState) Validate() error {
	// this line is used by starport scaffolding # genesis/types/validate

	if err := gs.Params.Validate(); err != nil {
		return err
	}
	if err := gs.GenesisOnlyParams.MaxIndividualPowerPercentage.Validate(); err != nil {
		return fmt.Errorf("genesis_only_params.max_individual_power_percentage: %w", err)
	}
	if err := gs.GenesisOnlyParams.GenesisGuardianMultiplier.Validate(); err != nil {
		return fmt.Errorf("genesis_only_params.genesis_guardian_multiplier: %w", err)
	}
	for i := range gs.ModelList {
		if err := gs.ModelList[i].ValidationThreshold.Validate(); err != nil {
			return fmt.Errorf("model_list[%d].validation_threshold: %w", i, err)
		}
	}
	for i := range gs.ParticipantList {
		stats := gs.ParticipantList[i].CurrentEpochStats
		if stats == nil {
			continue
		}
		if err := stats.InvalidLLR.Validate(); err != nil {
			return fmt.Errorf("participant_list[%d].current_epoch_stats.invalidLLR: %w", i, err)
		}
		if err := stats.InactiveLLR.Validate(); err != nil {
			return fmt.Errorf("participant_list[%d].current_epoch_stats.inactiveLLR: %w", i, err)
		}
		if err := stats.ConfirmationPoCRatio.Validate(); err != nil {
			return fmt.Errorf("participant_list[%d].current_epoch_stats.confirmationPoCRatio: %w", i, err)
		}
	}
	return nil
}
