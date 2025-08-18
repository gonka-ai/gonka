package types

import (
	"fmt"
	"strconv"

	sdk "github.com/cosmos/cosmos-sdk/types"
	paramtypes "github.com/cosmos/cosmos-sdk/x/params/types"
)

var _ paramtypes.ParamSet = (*Params)(nil)

// Parameter keys
var (
	KeyRestrictionEndBlock         = []byte("RestrictionEndBlock")
	KeyEmergencyTransferExemptions = []byte("EmergencyTransferExemptions")
	KeyExemptionUsageTracking      = []byte("ExemptionUsageTracking")
)

// Default parameter values
const (
	// Default: 0 (no restrictions) - for testing/testnet.
	// Production should set 1,555,000 blocks (~90 days) in genesis
	DefaultRestrictionEndBlock = uint64(0)
)

// ParamKeyTable the param key table for launch module
func ParamKeyTable() paramtypes.KeyTable {
	return paramtypes.NewKeyTable().RegisterParamSet(&Params{})
}

// NewParams creates a new Params instance
func NewParams() Params {
	return Params{
		RestrictionEndBlock:         DefaultRestrictionEndBlock,
		EmergencyTransferExemptions: []EmergencyTransferExemption{},
		ExemptionUsageTracking:      []ExemptionUsage{},
	}
}

// DefaultParams returns a default set of parameters
func DefaultParams() Params {
	return NewParams()
}

// ParamSetPairs get the params.ParamSet
func (p *Params) ParamSetPairs() paramtypes.ParamSetPairs {
	return paramtypes.ParamSetPairs{
		paramtypes.NewParamSetPair(KeyRestrictionEndBlock, &p.RestrictionEndBlock, validateRestrictionEndBlock),
		paramtypes.NewParamSetPair(KeyEmergencyTransferExemptions, &p.EmergencyTransferExemptions, validateEmergencyTransferExemptions),
		paramtypes.NewParamSetPair(KeyExemptionUsageTracking, &p.ExemptionUsageTracking, validateExemptionUsageTracking),
	}
}

// Validate validates the set of params
func (p Params) Validate() error {
	if err := validateRestrictionEndBlock(p.RestrictionEndBlock); err != nil {
		return err
	}
	if err := validateEmergencyTransferExemptions(p.EmergencyTransferExemptions); err != nil {
		return err
	}
	if err := validateExemptionUsageTracking(p.ExemptionUsageTracking); err != nil {
		return err
	}
	return nil
}

// validateRestrictionEndBlock validates the restriction end block parameter
func validateRestrictionEndBlock(i interface{}) error {
	_, ok := i.(uint64)
	if !ok {
		return fmt.Errorf("invalid parameter type: %T", i)
	}

	// 0 is valid (means no restrictions - for testing/testnet)
	// Any positive value is valid (restriction end block height)
	return nil
}

// validateEmergencyTransferExemptions validates the emergency transfer exemptions parameter
func validateEmergencyTransferExemptions(i interface{}) error {
	exemptions, ok := i.([]EmergencyTransferExemption)
	if !ok {
		return fmt.Errorf("invalid parameter type: %T", i)
	}

	// Validate each exemption
	for idx, exemption := range exemptions {
		if err := validateEmergencyTransferExemption(exemption); err != nil {
			return fmt.Errorf("invalid exemption at index %d: %w", idx, err)
		}
	}

	return nil
}

// validateEmergencyTransferExemption validates a single emergency transfer exemption
func validateEmergencyTransferExemption(exemption EmergencyTransferExemption) error {
	// Exemption ID must not be empty
	if exemption.ExemptionId == "" {
		return fmt.Errorf("exemption ID cannot be empty")
	}

	// From address must be valid or wildcard
	if exemption.FromAddress != "*" {
		if _, err := sdk.AccAddressFromBech32(exemption.FromAddress); err != nil {
			return fmt.Errorf("invalid from address: %w", err)
		}
	}

	// To address must be valid or wildcard
	if exemption.ToAddress != "*" {
		if _, err := sdk.AccAddressFromBech32(exemption.ToAddress); err != nil {
			return fmt.Errorf("invalid to address: %w", err)
		}
	}

	// Max amount must be valid
	if exemption.MaxAmount == "" {
		return fmt.Errorf("max amount cannot be empty")
	}
	if _, err := strconv.ParseUint(exemption.MaxAmount, 10, 64); err != nil {
		return fmt.Errorf("invalid max amount: %w", err)
	}

	// Usage limit must be greater than 0
	if exemption.UsageLimit == 0 {
		return fmt.Errorf("usage limit must be greater than 0")
	}

	// Expiry block must be greater than 0
	if exemption.ExpiryBlock == 0 {
		return fmt.Errorf("expiry block must be greater than 0")
	}

	// Justification should not be empty (warning, not error)
	if exemption.Justification == "" {
		// Just a warning, not an error - justification is recommended but not required
	}

	return nil
}

// validateExemptionUsageTracking validates the exemption usage tracking parameter
func validateExemptionUsageTracking(i interface{}) error {
	usages, ok := i.([]ExemptionUsage)
	if !ok {
		return fmt.Errorf("invalid parameter type: %T", i)
	}

	// Validate each usage entry
	for idx, usage := range usages {
		if err := validateExemptionUsage(usage); err != nil {
			return fmt.Errorf("invalid usage entry at index %d: %w", idx, err)
		}
	}

	return nil
}

// validateExemptionUsage validates a single exemption usage entry
func validateExemptionUsage(usage ExemptionUsage) error {
	// Exemption ID must not be empty
	if usage.ExemptionId == "" {
		return fmt.Errorf("exemption ID cannot be empty")
	}

	// Account address must be valid
	if _, err := sdk.AccAddressFromBech32(usage.AccountAddress); err != nil {
		return fmt.Errorf("invalid account address: %w", err)
	}

	// Usage count can be 0 (valid initial state)

	return nil
}
