package state

import (
	"testing"

	"github.com/stretchr/testify/require"

	"subnet/types"
)

func TestDeriveSeed_Basic(t *testing.T) {
	sig := make([]byte, 65)
	sig[0] = 0x01
	sig[7] = 0x42

	seed, err := DeriveSeed(sig)
	require.NoError(t, err)
	require.NotZero(t, seed)
	require.True(t, seed > 0, "seed must be positive")
}

func TestDeriveSeed_NonZero(t *testing.T) {
	// All zeros in first 8 bytes -> masked to 0, then forced to 1.
	sig := make([]byte, 65)
	seed, err := DeriveSeed(sig)
	require.NoError(t, err)
	require.Equal(t, int64(1), seed)
}

func TestDeriveSeed_TooShort(t *testing.T) {
	_, err := DeriveSeed([]byte{1, 2, 3})
	require.ErrorIs(t, err, types.ErrSeedTooShort)
}

func TestDeterministicFloat_Range(t *testing.T) {
	for seed := int64(1); seed <= 100; seed++ {
		for id := uint64(1); id <= 100; id++ {
			f := DeterministicFloat(seed, id)
			require.True(t, f >= 0 && f < 1, "float %f out of range for seed=%d id=%d", f, seed, id)
		}
	}
}

func TestDeterministicFloat_Deterministic(t *testing.T) {
	a := DeterministicFloat(42, 100)
	b := DeterministicFloat(42, 100)
	require.Equal(t, a, b)

	c := DeterministicFloat(42, 101)
	require.NotEqual(t, a, c, "different inputs should produce different outputs")
}

func TestShouldValidate_FullRate(t *testing.T) {
	// 10000 bp = 100%. With 1 validator slot and 2 non-executor slots,
	// probability = 1.0 * 1/2 = 0.5. Run many trials.
	trueCount := 0
	for id := uint64(1); id <= 1000; id++ {
		if ShouldValidate(12345, id, 1, 1, 3, 10000) {
			trueCount++
		}
	}
	require.True(t, trueCount > 400 && trueCount < 600,
		"expected ~50%% validation rate, got %d/1000", trueCount)
}

func TestShouldValidate_ZeroRate(t *testing.T) {
	for id := uint64(1); id <= 100; id++ {
		require.False(t, ShouldValidate(42, id, 1, 1, 3, 0))
	}
}

func TestShouldValidate_DivisionByZeroGuard(t *testing.T) {
	// totalSlots == executorSlotCount -> false.
	require.False(t, ShouldValidate(42, 1, 1, 3, 3, 10000))
	// totalSlots < executorSlotCount.
	require.False(t, ShouldValidate(42, 1, 1, 5, 3, 10000))
}
