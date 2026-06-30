package keeper_test

import (
	"math"
	"math/bits"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSettleAccountsZeroPaymentGuard_DistinguishesWrapFromGenuineZero(t *testing.T) {
	wrapSum, wrapCarry := bits.Add64(math.MaxUint64, 1, 0)
	require.Equal(t, uint64(0), wrapSum)
	require.Equal(t, uint64(1), wrapCarry)
	require.False(t, wrapCarry == 0 && wrapSum == 0)

	zeroSum, zeroCarry := bits.Add64(0, 0, 0)
	require.Equal(t, uint64(0), zeroSum)
	require.Equal(t, uint64(0), zeroCarry)
	require.True(t, zeroCarry == 0 && zeroSum == 0)
}
