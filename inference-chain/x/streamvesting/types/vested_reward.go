package types

import (
	"fmt"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

// VestedReward is one AddVestedRewards call inside AddVestedRewardsBatch.
type VestedReward struct {
	Amount        sdk.Coins
	VestingEpochs *uint64
	Memo          string
}

// VestedRewardError names which reward of a batch failed.
type VestedRewardError struct {
	Index int
	Err   error
}

func (e *VestedRewardError) Error() string {
	return fmt.Sprintf("vested reward %d: %v", e.Index, e.Err)
}

func (e *VestedRewardError) Unwrap() error { return e.Err }
