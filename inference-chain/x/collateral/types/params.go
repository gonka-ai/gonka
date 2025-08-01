package types

import (
	"fmt"

	paramtypes "github.com/cosmos/cosmos-sdk/x/params/types"
)

var _ paramtypes.ParamSet = (*Params)(nil)

// Default parameter values
var (
	DefaultUnbondingPeriodEpochs = uint64(1) // 1 epoch
)

// Parameter store keys
var (
	KeyUnbondingPeriodEpochs = []byte("UnbondingPeriodEpochs")
)

// ParamKeyTable the param key table for launch module
func ParamKeyTable() paramtypes.KeyTable {
	return paramtypes.NewKeyTable().RegisterParamSet(&Params{})
}

// NewParams creates a new Params instance
func NewParams(
	unbondingPeriodEpochs uint64,
) Params {
	return Params{
		UnbondingPeriodEpochs: unbondingPeriodEpochs,
	}
}

// DefaultParams returns a default set of parameters
func DefaultParams() Params {
	return NewParams(
		DefaultUnbondingPeriodEpochs,
	)
}

// ParamSetPairs get the params.ParamSet
func (p *Params) ParamSetPairs() paramtypes.ParamSetPairs {
	return paramtypes.ParamSetPairs{
		paramtypes.NewParamSetPair(KeyUnbondingPeriodEpochs, &p.UnbondingPeriodEpochs, validateUnbondingPeriodEpochs),
	}
}

// Validate validates the set of params
func (p Params) Validate() error {
	if err := validateUnbondingPeriodEpochs(p.UnbondingPeriodEpochs); err != nil {
		return err
	}
	return nil
}

// validateUnbondingPeriodEpochs validates the UnbondingPeriodEpochs param
func validateUnbondingPeriodEpochs(v interface{}) error {
	unbondingPeriodEpochs, ok := v.(uint64)
	if !ok {
		return fmt.Errorf("invalid parameter type: %T", v)
	}

	if unbondingPeriodEpochs == 0 {
		return fmt.Errorf("unbonding period epochs must be positive")
	}

	return nil
}
