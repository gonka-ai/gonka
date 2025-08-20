package types

import (
	"fmt"
	"strings"

	sdk "github.com/cosmos/cosmos-sdk/types"
	paramtypes "github.com/cosmos/cosmos-sdk/x/params/types"
)

var _ paramtypes.ParamSet = (*Params)(nil)

// Parameter store keys
var (
	KeyAllowedAccounts = []byte("AllowedAccounts")
	KeyRestrictToList  = []byte("RestrictToList")
)

// ParamKeyTable the param key table for launch module
func ParamKeyTable() paramtypes.KeyTable {
	return paramtypes.NewKeyTable().RegisterParamSet(&Params{})
}

// NewParams creates a new Params instance
func NewParams(allowedAccounts []string, restrictToList bool) Params {
	// Ensure AllowedAccounts is never nil
	if allowedAccounts == nil {
		allowedAccounts = []string{}
	}
	return Params{
		AllowedAccounts: allowedAccounts,
		RestrictToList:  restrictToList,
	}
}

// DefaultParams returns a default set of parameters
func DefaultParams() Params {
	params := NewParams([]string{}, false) // By default, no whitelist restrictions
	// Ensure AllowedAccounts is never nil
	if params.AllowedAccounts == nil {
		params.AllowedAccounts = []string{}
	}
	return params
}

// ParamSetPairs get the params.ParamSet
func (p *Params) ParamSetPairs() paramtypes.ParamSetPairs {
	return paramtypes.ParamSetPairs{
		paramtypes.NewParamSetPair(KeyAllowedAccounts, &p.AllowedAccounts, validateAllowedAccounts),
		paramtypes.NewParamSetPair(KeyRestrictToList, &p.RestrictToList, validateRestrictToList),
	}
}

// Validate validates the set of params
func (p Params) Validate() error {
	if err := validateAllowedAccounts(p.AllowedAccounts); err != nil {
		return err
	}
	if err := validateRestrictToList(p.RestrictToList); err != nil {
		return err
	}
	return nil
}

// validateAllowedAccounts validates the allowed accounts parameter
func validateAllowedAccounts(i interface{}) error {
	allowedAccounts, ok := i.([]string)
	if !ok {
		return fmt.Errorf("invalid parameter type: %T", i)
	}

	// Check for duplicates and validate addresses
	seen := make(map[string]bool)
	for _, addr := range allowedAccounts {
		// Check for empty addresses
		if strings.TrimSpace(addr) == "" {
			return fmt.Errorf("allowed account address cannot be empty")
		}

		// Check for duplicates
		if seen[addr] {
			return fmt.Errorf("duplicate allowed account address: %s", addr)
		}
		seen[addr] = true

		// Validate bech32 format
		if _, err := sdk.AccAddressFromBech32(addr); err != nil {
			return fmt.Errorf("invalid allowed account address %s: %w", addr, err)
		}
	}

	return nil
}

// validateRestrictToList validates the restrict to list parameter
func validateRestrictToList(i interface{}) error {
	_, ok := i.(bool)
	if !ok {
		return fmt.Errorf("invalid parameter type: %T", i)
	}
	// Boolean parameter doesn't need additional validation
	return nil
}

// Validate validates a transfer record
func (tr TransferRecord) Validate() error {
	// Validate genesis address
	if _, err := sdk.AccAddressFromBech32(tr.GenesisAddress); err != nil {
		return fmt.Errorf("invalid genesis address %s: %w", tr.GenesisAddress, err)
	}

	// Validate recipient address
	if _, err := sdk.AccAddressFromBech32(tr.RecipientAddress); err != nil {
		return fmt.Errorf("invalid recipient address %s: %w", tr.RecipientAddress, err)
	}

	// Validate transfer height
	if tr.TransferHeight == 0 {
		return fmt.Errorf("transfer height cannot be zero")
	}

	return nil
}
