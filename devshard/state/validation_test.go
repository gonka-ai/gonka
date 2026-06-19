package state

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/types"
)

const legacyPenaltyValidationRate = 10000

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

func TestUint64ProbabilityScale32(t *testing.T) {
	// 5000/10000 = 0.5 -> scale 32 is 2^31
	half := uint64ProbabilityScale32(5000, 10000)
	require.Equal(t, uint64(1)<<31, half)

	// 10000/10000 = 1.0 -> clamped to 2^32
	full := uint64ProbabilityScale32(10000, 10000)
	require.Equal(t, uint64(1)<<32, full)

	// 0 -> 0
	require.Equal(t, uint64(0), uint64ProbabilityScale32(0, 1))

	// Percent-style numerators: same as floor(n * 2^32 / 100) for n <= 100.
	require.Equal(t, uint64ProbabilityScale32(50, 100), (uint64(1<<32)*50)/100)   // 50% of 2^32
	require.Equal(t, uint64ProbabilityScale32(98, 100), (uint64(1<<32)*98)/100)   // 98% of 2^32
	require.Equal(t, uint64ProbabilityScale32(100, 100), (uint64(1<<32)*100)/100) // 100% of 2^32
	// Clamped to 2^32
	require.Equal(t, uint64ProbabilityScale32(105, 100), uint64ProbabilityScale32(100, 100)) // 100%

	// Oversized ratio: clamp to 2^32 (same as old ShouldValidate cap)
	clamped := uint64ProbabilityScale32(20000, 10000)
	require.Equal(t, uint64(1)<<32, clamped)

	// Numerator > 2^32-1: naive (numerator << 32) would wrap; 128-bit path must match the ratio.
	largeNum := uint64(1) << 40
	largeDen := uint64(1) << 41
	got := uint64ProbabilityScale32(largeNum, largeDen)
	require.Equal(t, uint64(1)<<31, got, "expect floor(2^40*2^32/2^41)=2^31")
}

func TestUint32CeilScaledSum32(t *testing.T) {
	cases := []struct {
		sum  uint64
		want uint32
	}{
		{0, 0},
		{1, 1},
		{1<<32 - 1, 1},
		{1 << 32, 1},
		{1<<32 + 1, 2},
		{(1 << 32) * 5, 5},
	}
	for _, tc := range cases {
		require.Equal(t, tc.want, uint32CeilScaledSum32(tc.sum), "sum=%d", tc.sum)
	}
}

// legacyPenalizeRequiredFloat matches pre-#893 penalizeUnrevealedSeeds: per-term
// probability capped at 1.0, then sum, then math.Ceil (test reference only).
func legacyPenalizeRequiredFloat(contributions []struct{ v, d uint64 }) uint32 {
	rate := float64(legacyPenaltyValidationRate) / 10000.0
	var probSum float64
	for _, c := range contributions {
		p := rate * float64(c.v) / float64(c.d)
		if p > 1.0 {
			p = 1.0
		}
		probSum += p
	}
	return uint32(math.Ceil(probSum))
}

func penalizeRequiredFromScaledTerms(contributions []struct{ v, d uint64 }) uint32 {
	var sumScaled uint64
	for _, c := range contributions {
		sumScaled += penalizePerInferenceScaled32(uint64(legacyPenaltyValidationRate), c.v, c.d)
	}
	return uint32CeilScaledSum32(sumScaled)
}

func TestPenalizePerInferenceMatchesLegacyFloatReference(t *testing.T) {
	// Single-inference cases: integer fixed-point path matches legacy float + Ceil.
	for d := uint64(1); d <= 64; d++ {
		for v := uint64(0); v <= 64; v++ {
			cs := []struct{ v, d uint64 }{{v: v, d: d}}
			leg := legacyPenalizeRequiredFloat(cs)
			got := penalizeRequiredFromScaledTerms(cs)
			require.Equal(t, leg, got, "v=%d d=%d", v, d)
		}
	}
}

func TestPenalizeRequiredMultiTermMatchesLegacyFloatReference(t *testing.T) {
	// Same aggregation order as machine.penalizeUnrevealedSeeds: sum scaled terms, then ceil.
	cases := [][]struct{ v, d uint64 }{
		{{1, 3}, {1, 3}},
		{{3, 7}, {2, 5}},
		{{1, 2}, {1, 3}, {2, 7}},
		{{10, 11}, {9, 10}, {1, 100}},
	}
	for i, cs := range cases {
		leg := legacyPenalizeRequiredFloat(cs)
		got := penalizeRequiredFromScaledTerms(cs)
		require.Equal(t, leg, got, "case %d", i)
	}
}

func TestDeterministicHash_Deterministic(t *testing.T) {
	a := deterministicHash(42, 100)
	b := deterministicHash(42, 100)
	require.Equal(t, a, b, "same input must produce same output")

	c := deterministicHash(42, 101)
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
