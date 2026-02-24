package public

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMergeModelTokenStatsForMissingModels_PrefersLocalAndFillsMissing(t *testing.T) {
	capacityMap := map[string]uint64{
		"model-a": 100,
		"model-b": 100,
		"model-c": 100,
	}
	localStats := modelTokenStats{
		"model-a": 10,
	}
	chainStats := modelTokenStats{
		"model-a": 999, // should not override local
		"model-b": 20,
		"model-c": 30,
		"model-d": 40, // should be ignored (not in capacity map)
	}

	merged := mergeModelTokenStatsForMissingModels(capacityMap, localStats, chainStats)

	require.Equal(t, int64(10), merged["model-a"])
	require.Equal(t, int64(20), merged["model-b"])
	require.Equal(t, int64(30), merged["model-c"])
	_, exists := merged["model-d"]
	require.False(t, exists)
}

func TestMergeModelTokenStatsForMissingModels_NoLocalStats(t *testing.T) {
	capacityMap := map[string]uint64{
		"model-a": 100,
	}
	var localStats modelTokenStats
	chainStats := modelTokenStats{
		"model-a": 77,
	}

	merged := mergeModelTokenStatsForMissingModels(capacityMap, localStats, chainStats)

	require.Equal(t, int64(77), merged["model-a"])
}

func TestHasMissingModelStats(t *testing.T) {
	capacityMap := map[string]uint64{
		"model-a": 100,
		"model-b": 100,
	}

	require.True(t, hasMissingModelStats(capacityMap, modelTokenStats{
		"model-a": 1,
	}))
	require.False(t, hasMissingModelStats(capacityMap, modelTokenStats{
		"model-a": 1,
		"model-b": 2,
	}))
}
