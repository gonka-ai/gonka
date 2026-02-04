package poc

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSampleLeafIndices_ZeroCount(t *testing.T) {
	result := sampleLeafIndices("pk", "hash", 100, 0, 10)
	assert.Nil(t, result)
}

func TestSampleLeafIndices_ZeroSampleSize(t *testing.T) {
	result := sampleLeafIndices("pk", "hash", 100, 1000, 0)
	assert.Nil(t, result)
}

func TestSampleLeafIndices_AllIndices(t *testing.T) {
	result := sampleLeafIndices("pk", "hash", 100, 5, 10)
	assert.Len(t, result, 5)
	for i, idx := range result {
		assert.Equal(t, uint32(i), idx)
	}
}

func TestSampleLeafIndices_Subset(t *testing.T) {
	result := sampleLeafIndices("pk", "hash", 100, 1000, 10)
	assert.Len(t, result, 10)
}

func TestSampleLeafIndices_NoDuplicates(t *testing.T) {
	result := sampleLeafIndices("pk", "hash", 100, 1000, 100)
	seen := make(map[uint32]bool, len(result))
	for _, idx := range result {
		assert.False(t, seen[idx], "duplicate index: %d", idx)
		seen[idx] = true
	}
}

func TestSampleLeafIndices_ValidRange(t *testing.T) {
	var count uint32 = 500
	result := sampleLeafIndices("pk", "hash", 100, count, 50)
	for _, idx := range result {
		assert.True(t, idx < count)
	}
}

func TestSampleLeafIndices_Deterministic(t *testing.T) {
	r1 := sampleLeafIndices("pk", "hash", 100, 10000, 50)
	r2 := sampleLeafIndices("pk", "hash", 100, 10000, 50)
	assert.Equal(t, r1, r2)
}

func TestSampleLeafIndices_DifferentSeed(t *testing.T) {
	r1 := sampleLeafIndices("pk1", "hash", 100, 10000, 50)
	r2 := sampleLeafIndices("pk2", "hash", 100, 10000, 50)
	assert.NotEqual(t, r1, r2)
}

func TestSampleLeafIndices_LargeCount(t *testing.T) {
	result := sampleLeafIndices("pk", "hash", 100, 100_000_000, 200)
	assert.Len(t, result, 200)

	seen := make(map[uint32]bool, len(result))
	for _, idx := range result {
		assert.True(t, idx < 100_000_000)
		assert.False(t, seen[idx])
		seen[idx] = true
	}
}
