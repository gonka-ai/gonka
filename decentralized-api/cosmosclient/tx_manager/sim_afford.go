package tx_manager

import (
	"strings"
	"time"

	"cosmossdk.io/math"
	"cosmossdk.io/x/feegrant"
	sdk "github.com/cosmos/cosmos-sdk/types"

	inferencetypes "github.com/productscience/inference/x/inference/types"
)

// maxAffordableGas is min(BatchGasLimit, spendable/price): the largest
// gas_limit whose fee still fits. price <= 0 means no coins are charged.
func maxAffordableGas(spendable math.Int, price int64) uint64 {
	if price <= 0 {
		return BatchGasLimit
	}
	if !spendable.IsPositive() {
		return 0
	}
	quot := spendable.QuoRaw(price)
	if !quot.IsPositive() {
		return 0
	}
	if quot.GT(math.NewIntFromUint64(BatchGasLimit)) {
		return BatchGasLimit
	}
	return quot.Uint64()
}

func ngonkaOf(coins sdk.Coins) math.Int {
	if coins == nil {
		return math.ZeroInt()
	}
	return coins.AmountOf(inferencetypes.BaseCoin)
}

func isInsufficientFundsErr(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "insufficient funds")
}

// feegrantRemaining is remaining ngonka on the allowance, or -1 if unlimited.
func feegrantRemaining(allowance feegrant.FeeAllowanceI, now time.Time) math.Int {
	if allowance == nil {
		return math.ZeroInt()
	}
	unlimited := math.NewInt(-1)
	switch a := allowance.(type) {
	case *feegrant.BasicAllowance:
		if a.Expiration != nil && !a.Expiration.After(now) {
			return math.ZeroInt()
		}
		if a.SpendLimit == nil || a.SpendLimit.IsZero() {
			return unlimited
		}
		return ngonkaOf(a.SpendLimit)
	case *feegrant.PeriodicAllowance:
		if a.Basic.Expiration != nil && !a.Basic.Expiration.After(now) {
			return math.ZeroInt()
		}
		remaining := unlimited
		if a.Basic.SpendLimit != nil && !a.Basic.SpendLimit.IsZero() {
			remaining = ngonkaOf(a.Basic.SpendLimit)
		}
		period := ngonkaOf(a.PeriodCanSpend)
		if remaining.IsNegative() {
			return period
		}
		if period.LT(remaining) {
			return period
		}
		return remaining
	default:
		return unlimited
	}
}
