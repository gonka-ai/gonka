package types

import (
	"math"
	"math/bits"
)

func (sa *SettleAmount) GetTotalCoins() int64 {
	sum, carry := bits.Add64(sa.RewardCoins, sa.WorkCoins, 0)
	if carry != 0 || sum > math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(sum)
}
