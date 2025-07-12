package types

import (
	"fmt"

	"cosmossdk.io/math"
	paramtypes "github.com/cosmos/cosmos-sdk/x/params/types"
)

var _ paramtypes.ParamSet = (*Params)(nil)

// Default parameter values
var (
	DefaultBaseWeightRatio         = math.LegacyNewDecWithPrec(2, 1) // 0.2 (20%)
	DefaultCollateralPerWeightUnit = math.LegacyNewDec(1)            // 1 token per weight unit
	DefaultUnbondingPeriodEpochs   = uint64(1)                       // 1 epoch
)

// Parameter store keys
var (
	KeyBaseWeightRatio         = []byte("BaseWeightRatio")
	KeyCollateralPerWeightUnit = []byte("CollateralPerWeightUnit")
	KeyUnbondingPeriodEpochs   = []byte("UnbondingPeriodEpochs")
)

// ParamKeyTable the param key table for launch module
func ParamKeyTable() paramtypes.KeyTable {
	return paramtypes.NewKeyTable().RegisterParamSet(&Params{})
}

// NewParams creates a new Params instance
func NewParams(
	baseWeightRatio math.LegacyDec,
	collateralPerWeightUnit math.LegacyDec,
	unbondingPeriodEpochs uint64,
) Params {
	return Params{
		BaseWeightRatio:         baseWeightRatio,
		CollateralPerWeightUnit: collateralPerWeightUnit,
		UnbondingPeriodEpochs:   unbondingPeriodEpochs,
	}
}

// DefaultParams returns a default set of parameters
func DefaultParams() Params {
	return NewParams(
		DefaultBaseWeightRatio,
		DefaultCollateralPerWeightUnit,
		DefaultUnbondingPeriodEpochs,
	)
}

// ParamSetPairs get the params.ParamSet
func (p *Params) ParamSetPairs() paramtypes.ParamSetPairs {
	return paramtypes.ParamSetPairs{
		paramtypes.NewParamSetPair(KeyBaseWeightRatio, &p.BaseWeightRatio, validateBaseWeightRatio),
		paramtypes.NewParamSetPair(KeyCollateralPerWeightUnit, &p.CollateralPerWeightUnit, validateCollateralPerWeightUnit),
		paramtypes.NewParamSetPair(KeyUnbondingPeriodEpochs, &p.UnbondingPeriodEpochs, validateUnbondingPeriodEpochs),
	}
}

// Validate validates the set of params
func (p Params) Validate() error {
	if err := validateBaseWeightRatio(p.BaseWeightRatio); err != nil {
		return err
	}
	if err := validateCollateralPerWeightUnit(p.CollateralPerWeightUnit); err != nil {
		return err
	}
	if err := validateUnbondingPeriodEpochs(p.UnbondingPeriodEpochs); err != nil {
		return err
	}
	return nil
}

// validateBaseWeightRatio validates the BaseWeightRatio param
func validateBaseWeightRatio(v interface{}) error {
	baseWeightRatio, ok := v.(math.LegacyDec)
	if !ok {
		return fmt.Errorf("invalid parameter type: %T", v)
	}

	if baseWeightRatio.IsNegative() {
		return fmt.Errorf("base weight ratio cannot be negative: %s", baseWeightRatio)
	}

	if baseWeightRatio.GT(math.LegacyOneDec()) {
		return fmt.Errorf("base weight ratio cannot be greater than 1: %s", baseWeightRatio)
	}

	return nil
}

// validateCollateralPerWeightUnit validates the CollateralPerWeightUnit param
func validateCollateralPerWeightUnit(v interface{}) error {
	collateralPerWeightUnit, ok := v.(math.LegacyDec)
	if !ok {
		return fmt.Errorf("invalid parameter type: %T", v)
	}

	if collateralPerWeightUnit.IsNegative() {
		return fmt.Errorf("collateral per weight unit cannot be negative: %s", collateralPerWeightUnit)
	}

	if collateralPerWeightUnit.IsZero() {
		return fmt.Errorf("collateral per weight unit cannot be zero")
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
