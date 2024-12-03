package types

func (sa *SettleAmount) GetTotalCoins() uint64 {
	return sa.RefundCoins + sa.RewardCoins + sa.WorkCoins
}
