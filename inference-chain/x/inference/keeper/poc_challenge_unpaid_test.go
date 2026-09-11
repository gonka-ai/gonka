package keeper

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestChallengeRewardShareUsesBigInt(t *testing.T) {
	amount := int64(20_000_000)
	weight := uint64(1) << 40
	total := uint64(1) << 10
	wrapped := uint64(amount) * weight / total
	got := challengeRewardShare(amount, weight, total)
	require.NotEqual(t, wrapped, got)
	require.Equal(t, uint64(amount)<<30, got)
}
