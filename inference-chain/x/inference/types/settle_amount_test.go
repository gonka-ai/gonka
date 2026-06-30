package types

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSettleAmountGetTotalCoins(t *testing.T) {
	tests := []struct {
		name string
		sa   SettleAmount
		want int64
	}{
		{
			name: "normal sum",
			sa:   SettleAmount{RewardCoins: 1, WorkCoins: 2},
			want: 3,
		},
		{
			name: "max int64 unchanged",
			sa:   SettleAmount{RewardCoins: math.MaxInt64, WorkCoins: 0},
			want: math.MaxInt64,
		},
		{
			name: "above int64 saturates",
			sa:   SettleAmount{RewardCoins: math.MaxInt64, WorkCoins: 1},
			want: math.MaxInt64,
		},
		{
			name: "uint64 wrap saturates",
			sa:   SettleAmount{RewardCoins: math.MaxUint64, WorkCoins: 1},
			want: math.MaxInt64,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, tt.sa.GetTotalCoins())
		})
	}
}
