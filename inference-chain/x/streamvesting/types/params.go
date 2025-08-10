package types

import (
	"fmt"

	paramtypes "github.com/cosmos/cosmos-sdk/x/params/types"
)

var _ paramtypes.ParamSet = (*Params)(nil)

// Parameter store keys
var (
	KeyRewardVestingPeriod = []byte("RewardVestingPeriod")
)

// ParamKeyTable the param key table for launch module
func ParamKeyTable() paramtypes.KeyTable {
	return paramtypes.NewKeyTable().RegisterParamSet(&Params{})
}

// NewParams creates a new Params instance
func NewParams(rewardVestingPeriod uint64) Params {
	return Params{
		RewardVestingPeriod: rewardVestingPeriod,
	}
}

// DefaultParams returns a default set of parameters
func DefaultParams() Params {
	return NewParams(180) // Default 180 epochs
}

// ParamSetPairs get the params.ParamSet
func (p *Params) ParamSetPairs() paramtypes.ParamSetPairs {
	return paramtypes.ParamSetPairs{
		paramtypes.NewParamSetPair(KeyRewardVestingPeriod, &p.RewardVestingPeriod, validateRewardVestingPeriod),
	}
}

// Validate validates the set of params
func (p Params) Validate() error {
	if err := validateRewardVestingPeriod(p.RewardVestingPeriod); err != nil {
		return err
	}
	return nil
}

// validateRewardVestingPeriod validates the RewardVestingPeriod param
func validateRewardVestingPeriod(v interface{}) error {
	rewardVestingPeriod, ok := v.(uint64)
	if !ok {
		return fmt.Errorf("invalid parameter type: %T", v)
	}

	if rewardVestingPeriod == 0 {
		return fmt.Errorf("reward vesting period must be positive: %d", rewardVestingPeriod)
	}

	return nil
}
