package types_test

import (
	"math"
	"testing"

	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func TestGetTotalCoins(t *testing.T) {
	tests := []struct {
		name        string
		rewardCoins uint64
		workCoins   uint64
		expected    int64
	}{
		{
			name:        "normal values",
			rewardCoins: 100,
			workCoins:   200,
			expected:    300,
		},
		{
			name:        "zero values",
			rewardCoins: 0,
			workCoins:   0,
			expected:    0,
		},
		{
			name:        "sum exceeds MaxInt64",
			rewardCoins: math.MaxInt64,
			workCoins:   1,
			expected:    math.MaxInt64,
		},
		{
			name:        "both at MaxInt64 triggers overflow cap",
			rewardCoins: math.MaxInt64,
			workCoins:   math.MaxInt64,
			expected:    math.MaxInt64,
		},
		{
			name:        "uint64 overflow wraps around",
			rewardCoins: math.MaxUint64,
			workCoins:   1,
			expected:    math.MaxInt64,
		},
		{
			name:        "large values near uint64 max",
			rewardCoins: math.MaxUint64 - 5,
			workCoins:   10,
			expected:    math.MaxInt64,
		},
		{
			name:        "exactly MaxInt64",
			rewardCoins: math.MaxInt64 - 100,
			workCoins:   100,
			expected:    math.MaxInt64,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sa := &types.SettleAmount{
				RewardCoins: tc.rewardCoins,
				WorkCoins:   tc.workCoins,
			}
			result := sa.GetTotalCoins()
			require.Equal(t, tc.expected, result)
		})
	}
}
