package cmd

import (
	"bytes"
	"strings"
	"testing"

	inferencetypes "github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func TestAddInferenceKey(t *testing.T) {
	buckets := map[string]*prefixStat{}
	catalog := inferencetypes.StatePrefixCatalog()

	participantKey := append([]byte(inferencetypes.ParticipantsPrefix), []byte("address")...)
	addInferenceKey(buckets, catalog, participantKey, int64(len(participantKey)), 20)
	addInferenceKey(buckets, catalog, []byte("TrainingTask/sync/1/value"), 25, 10)
	addInferenceKey(buckets, catalog, []byte{0xff, 0x01}, 2, 3)

	require.Equal(t, int64(1), buckets["Participants"].count)
	require.False(t, buckets["Participants"].legacy)
	require.Equal(t, int64(1), buckets["TrainingTaskSync"].count)
	require.True(t, buckets["TrainingTaskSync"].legacy)
	require.Equal(t, int64(1), buckets["<unmatched:0xff>"].count)
	require.True(t, buckets["<unmatched:0xff>"].unmatched)
}

func TestPrintInferenceBreakdownFiltersBeforeTopLimit(t *testing.T) {
	stats := []prefixStat{
		{name: "large-live", count: 1, valBytes: 100},
		{name: "large-legacy", legacy: true, count: 2, valBytes: 80},
		{name: "small-legacy", legacy: true, count: 3, valBytes: 20},
	}

	var out bytes.Buffer
	printInferenceBreakdown(&out, stats, 1, true)

	text := out.String()
	require.Contains(t, text, "large-legacy")
	require.NotContains(t, text, "large-live")
	require.NotContains(t, text, "small-legacy")
	require.Equal(t, 1, strings.Count(text, "yes"))
}

func TestHumanizeBytes(t *testing.T) {
	require.Equal(t, "0 B", humanizeBytes(0))
	require.Equal(t, "1023 B", humanizeBytes(1023))
	require.Equal(t, "1.0 KiB", humanizeBytes(1024))
	require.Equal(t, "1.5 MiB", humanizeBytes(1536*1024))
}
