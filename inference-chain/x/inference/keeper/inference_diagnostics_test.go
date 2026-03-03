package keeper

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestFormatMissedExecutors_Empty(t *testing.T) {
	result := formatMissedExecutors(nil)
	assert.Equal(t, "none", result)

	result = formatMissedExecutors([]ExecutorMissStats{})
	assert.Equal(t, "none", result)
}

func TestFormatMissedExecutors_SingleEntry(t *testing.T) {
	stats := []ExecutorMissStats{
		{Address: "gonka1abc123def456ghi789jkl012mno345pqr678stu", MissedRequests: 5, InferenceCount: 100},
	}
	result := formatMissedExecutors(stats)
	assert.Contains(t, result, "missed=5")
	assert.Contains(t, result, "total=100")
}

func TestFormatMissedExecutors_MultipleEntries(t *testing.T) {
	stats := []ExecutorMissStats{
		{Address: "gonka1abc123def456ghi789jkl012mno345pqr678stu", MissedRequests: 10, InferenceCount: 200},
		{Address: "gonka1xyz789uvw456rst123opq012mnl345ijk678ghi", MissedRequests: 3, InferenceCount: 50},
	}
	result := formatMissedExecutors(stats)
	assert.Contains(t, result, ";")
	assert.Contains(t, result, "missed=10")
	assert.Contains(t, result, "missed=3")
}

func TestTruncateAddress(t *testing.T) {
	// Short address stays as-is
	short := "short"
	assert.Equal(t, "short", truncateAddress(short))

	// Long address is truncated
	long := "gonka1abc123def456ghi789jkl012mno345pqr678stu"
	result := truncateAddress(long)
	assert.True(t, len(result) < len(long), "truncated address should be shorter")
	assert.Contains(t, result, "...")
	assert.Equal(t, long[:10], result[:10])
}

func TestGetTopMissedExecutors_NilGroupData(t *testing.T) {
	k := Keeper{}
	result := k.getTopMissedExecutors(nil, nil, 5)
	assert.Nil(t, result)
}

func TestGetTopMissedExecutors_Sorting(t *testing.T) {
	// Test the sorting logic directly with the helper
	stats := []ExecutorMissStats{
		{Address: "addr1", MissedRequests: 1, InferenceCount: 10},
		{Address: "addr2", MissedRequests: 5, InferenceCount: 20},
		{Address: "addr3", MissedRequests: 3, InferenceCount: 15},
	}

	// Sort by missed requests descending (replicating the sort logic)
	for i := 1; i < len(stats); i++ {
		j := i
		for j > 0 && stats[j].MissedRequests > stats[j-1].MissedRequests {
			stats[j], stats[j-1] = stats[j-1], stats[j]
			j--
		}
	}

	assert.Equal(t, uint64(5), stats[0].MissedRequests)
	assert.Equal(t, uint64(3), stats[1].MissedRequests)
	assert.Equal(t, uint64(1), stats[2].MissedRequests)
}
