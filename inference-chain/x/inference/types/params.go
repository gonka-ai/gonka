package types

import (
	paramtypes "github.com/cosmos/cosmos-sdk/x/params/types"
)

var _ paramtypes.ParamSet = (*Params)(nil)

// ParamKeyTable the param key table for launch module
func ParamKeyTable() paramtypes.KeyTable {
	return paramtypes.NewKeyTable().RegisterParamSet(&Params{})
}

// NewParams creates a new Params instance
func NewParams() Params {
	return Params{}
}

// DefaultParams returns a default set of parameters
func DefaultParams() Params {
	return Params{
		EpochParams: &EpochParams{
			EpochLength:         40,
			EpochMultiplier:     1,
			EpochNewCoin:        1_048_576,
			CoinHalvingInterval: 100,
		},
		ValidationParams: &ValidationParams{
			FalsePositiveRate:     0.05,
			MinRampUpMeasurements: 10,
			PassValue:             0.99,
		},
		PocParams: &PocParams{
			DefaultDifficulty: 5,
		},
	}
}

// ParamSetPairs get the params.ParamSet: Pretty sure this is deprecated
func (p *Params) ParamSetPairs() paramtypes.ParamSetPairs {
	return paramtypes.ParamSetPairs{}
}
func validateEpochParams(i interface{}) error {
	return nil
}

// Validate validates the set of params
func (p Params) Validate() error {
	return nil
}
