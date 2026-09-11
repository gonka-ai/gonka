package pocchallenge

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCountedSlices_ShortSegmentAutoPassed(t *testing.T) {
	require.Empty(t, CountedSlices(100, 250, 500))
	require.Empty(t, CountedSlices(100, 399, 500))
}

func TestCountedSlices_CompleteAndRemainder(t *testing.T) {
	// 500-block slice, 800-block segment: one full slice, remainder 300 counted.
	got := CountedSlices(100, 900, 500)
	require.Equal(t, []SliceRange{
		{Index: 0, Start: 100, End: 600, Length: 500},
		{Index: 1, Start: 600, End: 900, Length: 300},
	}, got)
}

func TestCountedSlices_DropsShortRemainder(t *testing.T) {
	got := CountedSlices(100, 750, 500)
	require.Equal(t, []SliceRange{
		{Index: 0, Start: 100, End: 600, Length: 500},
	}, got)
}

func TestCurrentAndLastSliceIndex(t *testing.T) {
	require.Equal(t, uint32(0), CurrentSliceIndex(100, 100, 500))
	require.Equal(t, uint32(1), CurrentSliceIndex(600, 100, 500))
	require.Equal(t, uint32(1), LastSliceIndex(100, 900, 500))
	require.Equal(t, uint32(0), LastSliceIndex(100, 250, 500))
}

func TestSafetyWindowHeight(t *testing.T) {
	require.Equal(t, int64(1950), SafetyWindowHeight(2000, 50))
}
