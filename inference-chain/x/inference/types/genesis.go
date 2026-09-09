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

	params := gs.Params
	if params.DevshardEscrowParams != nil && len(params.DevshardEscrowParams.ApprovedVersions) > 0 {
		cloned := *params.DevshardEscrowParams
		cloned.ApprovedVersions = nil
		params.DevshardEscrowParams = &cloned
	}
	if err := params.Validate(); err != nil {
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
	versions := gs.DevshardApprovedVersions
	if len(versions) == 0 && gs.Params.DevshardEscrowParams != nil {
		versions = gs.Params.DevshardEscrowParams.ApprovedVersions
	}
	if len(versions) > MaxDevshardApprovedVersions {
		return fmt.Errorf("devshard_approved_versions exceeds maximum of %d", MaxDevshardApprovedVersions)
	}
	seenApproved := make(map[string]struct{}, len(versions))
	for i, v := range versions {
		if v == nil {
			return fmt.Errorf("devshard_approved_versions[%d] cannot be null", i)
		}
		if err := v.Validate(); err != nil {
			return fmt.Errorf("devshard_approved_versions[%d]: %w", i, err)
		}
		if _, ok := seenApproved[v.Name]; ok {
			return fmt.Errorf("devshard_approved_versions: duplicate name %q", v.Name)
		}
		seenApproved[v.Name] = struct{}{}
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
