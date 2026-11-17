package main

import (
	"devshard/testenv/loadtest"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"testing"
)

func TestPrintMetricsSummaryShowsValuesAndMissingMetrics(t *testing.T) {
	path := filepath.Join(t.TempDir(), "summary.log")
	file, err := os.Create(path)
	require.NoError(t, err)
	prior := os.Stdout
	os.Stdout = file
	t.Cleanup(func() { os.Stdout = prior; _ = file.Close() })
	printMetricsSummary(loadtest.MetricsSummary{Interval: "1s", Processes: []loadtest.ProcessMetricsSummary{{
		Target: "gateway", Samples: 3,
		First:         map[string]float64{"process_resident_memory_bytes": 1 << 20},
		Last:          map[string]float64{"process_resident_memory_bytes": 2 << 20},
		Peaks:         map[string]float64{"process_resident_memory_bytes": 4 << 20},
		WorkloadPeaks: map[string]float64{"process_resident_memory_bytes": 3 << 20},
		Deltas:        map[string]float64{"process_cpu_seconds_total": 2},
		FailedSamples: 1, LastErrors: []string{"missing go_goroutines"},
	}}}, 1000)
	os.Stdout = prior
	require.NoError(t, file.Close())
	body, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(body), "baseline=1.000 MiB final=2.000 MiB workload peak=3.000 MiB all-phase peak=4.000 MiB")
	require.Contains(t, string(body), "RSS high-water mark: unavailable")
	require.Contains(t, string(body), "incomplete/failed=1")
	require.Contains(t, string(body), "CPU per 1000 completed client requests: 2.000 s")
	require.Contains(t, string(body), "missing go_goroutines")
}
