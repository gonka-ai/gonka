package types

import "math"

func (sa *SettleAmount) GetTotalCoins() int64 {
	sum := sa.RewardCoins + sa.WorkCoins
	// Check for uint64 addition overflow (wrap-around)
	if sum < sa.RewardCoins || sum < sa.WorkCoins {
		return math.MaxInt64
	}
	if sum > math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(sum)
}
