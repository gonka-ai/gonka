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

func feeDenomAmount(coins sdk.Coins) math.Int {
	if coins == nil {
		return math.ZeroInt()
	}
	return coins.AmountOf(inferencetypes.BaseCoin)
}

func isInsufficientFundsErr(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "insufficient funds")
}

// feegrantRemaining is remaining fee-denom on the allowance, or -1 if unlimited.
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
		return feeDenomAmount(a.SpendLimit)
	case *feegrant.PeriodicAllowance:
		if a.Basic.Expiration != nil && !a.Basic.Expiration.After(now) {
			return math.ZeroInt()
		}
		remaining := unlimited
		if a.Basic.SpendLimit != nil && !a.Basic.SpendLimit.IsZero() {
			remaining = feeDenomAmount(a.Basic.SpendLimit)
		}
		period := feeDenomAmount(periodicCanSpend(a, now))
		if remaining.IsNegative() {
			return period
		}
		if period.LT(remaining) {
			return period
		}
		return remaining
	case *feegrant.AllowedMsgAllowance:
		if a == nil || a.Allowance == nil {
			return math.ZeroInt()
		}
		inner, err := a.GetAllowance()
		if err != nil || inner == nil {
			return math.ZeroInt()
		}
		return feegrantRemaining(inner, now)
	default:
		return math.ZeroInt()
	}
}

// periodicCanSpend mirrors feegrant.PeriodicAllowance.tryResetPeriod: once
// PeriodReset is reached, PeriodCanSpend in state is stale until the next
// Accept writes the grant. Use the post-reset top-up for spendable queries.
func periodicCanSpend(a *feegrant.PeriodicAllowance, now time.Time) sdk.Coins {
	if a == nil {
		return nil
	}
	if a.PeriodReset.IsZero() || now.Before(a.PeriodReset) {
		return a.PeriodCanSpend
	}
	if a.Basic.SpendLimit != nil && !a.Basic.SpendLimit.Empty() {
		if _, isNeg := a.Basic.SpendLimit.SafeSub(a.PeriodSpendLimit...); isNeg {
			return a.Basic.SpendLimit
		}
	}
	return a.PeriodSpendLimit
}
