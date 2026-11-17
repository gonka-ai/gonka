package headertime

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEpochFloor(t *testing.T) {
	require.Equal(t, uint64(0), EpochFloor(0, 100))
	require.Equal(t, uint64(0), EpochFloor(1, 100))
	require.Equal(t, uint64(0), EpochFloor(2, 0), "unknown previous start keeps the map")
	require.Equal(t, uint64(17280), EpochFloor(2, 17280))
	require.Equal(t, uint64(34560), EpochFloor(5, 34560))
}
