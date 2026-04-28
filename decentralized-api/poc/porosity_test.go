package poc

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAbsInt32(t *testing.T) {
	tests := []struct {
		name string
		in   int32
		want int64
	}{
		{"zero", 0, 0},
		{"positive", 42, 42},
		{"negative", -42, 42},
		{"max_int32", math.MaxInt32, math.MaxInt32},
		{"min_int32", math.MinInt32, math.MaxInt32 + 1},
		{"minus_one", -1, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, absInt32(tt.in))
		})
	}
}

func TestMaxNonceValue(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		assert.Equal(t, int64(0), maxNonceValue(nil))
		assert.Equal(t, int64(0), maxNonceValue([]VerifiedArtifact{}))
	})

	t.Run("single_positive", func(t *testing.T) {
		assert.Equal(t, int64(10), maxNonceValue([]VerifiedArtifact{{Nonce: 10}}))
	})

	t.Run("single_negative", func(t *testing.T) {
		assert.Equal(t, int64(10), maxNonceValue([]VerifiedArtifact{{Nonce: -10}}))
	})

	t.Run("picks_largest_abs_from_negative", func(t *testing.T) {
		arts := []VerifiedArtifact{{Nonce: 5}, {Nonce: -100}, {Nonce: 50}}
		assert.Equal(t, int64(100), maxNonceValue(arts))
	})

	t.Run("min_int32", func(t *testing.T) {
		arts := []VerifiedArtifact{{Nonce: 1}, {Nonce: math.MinInt32}}
		assert.Equal(t, int64(math.MaxInt32)+1, maxNonceValue(arts))
	})
}

func makeArtifacts(nonces []int32) []VerifiedArtifact {
	arts := make([]VerifiedArtifact, len(nonces))
	for i, n := range nonces {
		arts[i] = VerifiedArtifact{LeafIndex: uint32(i), Nonce: n}
	}
	return arts
}

func TestIsPorosityTooHigh_SequentialNonces(t *testing.T) {
	nonces := make([]int32, 100)
	for i := range nonces {
		nonces[i] = int32(i + 1)
	}
	arts := makeArtifacts(nonces)
	maxNonce, porosity, tooHigh := isPorosityTooHigh(arts, 100)
	assert.False(t, tooHigh)
	assert.Equal(t, int64(100), maxNonce)
	assert.InDelta(t, 1.0, porosity, 0.001)
}

func TestIsPorosityTooHigh_SparseNonces(t *testing.T) {
	nonces := []int32{1, 10, 11, 15, 20, 25, 30, 40, 55, 60}
	remaining := 100 - len(nonces)
	for i := 0; i < remaining; i++ {
		nonces = append(nonces, int32(70+i*3))
	}
	arts := makeArtifacts(nonces)

	maxN := maxNonceValue(arts)
	require.Greater(t, maxN, int64(200))

	_, porosity, tooHigh := isPorosityTooHigh(arts, 100)
	assert.False(t, tooHigh)
	assert.Less(t, porosity, porosityThreshold)
}

func TestIsPorosityTooHigh_PositiveNonces(t *testing.T) {
	nonces := make([]int32, 100)
	for i := range nonces {
		nonces[i] = int32(1_000_000_000 + i)
	}
	arts := makeArtifacts(nonces)
	_, porosity, tooHigh := isPorosityTooHigh(arts, 100)
	assert.True(t, tooHigh)
	assert.GreaterOrEqual(t, porosity, porosityThreshold)
}

func TestIsPorosityTooHigh_NegativeNonces(t *testing.T) {
	nonces := make([]int32, 100)
	for i := range nonces {
		nonces[i] = int32(-1_000_000_000 - i)
	}
	arts := makeArtifacts(nonces)
	_, porosity, tooHigh := isPorosityTooHigh(arts, 100)
	assert.True(t, tooHigh)
	assert.GreaterOrEqual(t, porosity, porosityThreshold)
}

func TestIsPorosityTooHigh_MinInt32(t *testing.T) {
	arts := makeArtifacts([]int32{1, 2, math.MinInt32})
	_, porosity, tooHigh := isPorosityTooHigh(arts, 100)
	assert.True(t, tooHigh)
	assert.GreaterOrEqual(t, porosity, porosityThreshold)
}

func TestIsPorosityTooHigh_MixedPositiveNegative(t *testing.T) {
	nonces := make([]int32, 100)
	for i := range nonces {
		if i%2 == 0 {
			nonces[i] = int32(i + 1)
		} else {
			nonces[i] = -int32(i + 1)
		}
	}
	arts := makeArtifacts(nonces)
	_, porosity, tooHigh := isPorosityTooHigh(arts, uint32(len(nonces)))
	assert.False(t, tooHigh, "small mixed positive/negative nonces should pass")
	assert.InDelta(t, 1.0, porosity, 0.01)
}

func TestIsPorosityTooHigh_EdgeCases(t *testing.T) {
	t.Run("empty_artifacts", func(t *testing.T) {
		_, porosity, tooHigh := isPorosityTooHigh(nil, 100)
		assert.False(t, tooHigh)
		assert.Equal(t, 0.0, porosity)
	})

	t.Run("zero_count", func(t *testing.T) {
		arts := makeArtifacts([]int32{1})
		_, porosity, tooHigh := isPorosityTooHigh(arts, 0)
		assert.False(t, tooHigh)
		assert.Equal(t, 0.0, porosity)
	})

	t.Run("exactly_at_threshold", func(t *testing.T) {
		arts := makeArtifacts([]int32{10000})
		_, porosity, tooHigh := isPorosityTooHigh(arts, 100)
		assert.True(t, tooHigh)
		assert.Equal(t, porosityThreshold, porosity)
	})

	t.Run("just_below_threshold", func(t *testing.T) {
		arts := makeArtifacts([]int32{9999})
		_, porosity, tooHigh := isPorosityTooHigh(arts, 100)
		assert.False(t, tooHigh)
		assert.Less(t, porosity, porosityThreshold)
	})
}
