package types

func (sa *SettleAmount) GetTotalCoins() uint64 {
	return sa.RewardCoins + sa.WorkCoins
}
