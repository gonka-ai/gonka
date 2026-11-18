package types

import (
	"fmt"
)

// Parameter store keys
var (
	KeyRewardVestingPeriod = []byte("RewardVestingPeriod")
)

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
