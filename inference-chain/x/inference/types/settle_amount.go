package types

import (
	"math"
	"math/bits"
)

// saturate to MaxInt64 on overflow — a wrapped sum can silently zero a real payout downstream.
func (sa *SettleAmount) GetTotalCoins() int64 {
	sum, carry := bits.Add64(sa.RewardCoins, sa.WorkCoins, 0)
	if carry != 0 || sum > math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(sum)
}
