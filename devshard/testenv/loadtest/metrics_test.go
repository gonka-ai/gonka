package loadtest

import (
	"context"
	"encoding/json"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestProbeDiagnosticsDoNotCorruptMetrics(t *testing.T) {
	cmd := exec.Command("sh", "-c", "printf 'line diagnostic\\n' >&2; printf 'go_goroutines 7\\n'")
	body, err := probeCommandOutput(cmd, "gateway")
	require.NoError(t, err)
	require.Equal(t, "go_goroutines 7\n", body)
	values, _, err := parseRuntimeMetrics(body)
	require.NoError(t, err)
	require.Equal(t, 7.0, values["go_goroutines"])
	cmd = exec.Command("sh", "-c", "printf 'probe failed\\n' >&2; exit 1")
	body, err = probeCommandOutput(cmd, "gateway")
	require.Empty(t, body)
	require.ErrorContains(t, err, "probe failed")
}

func TestParseRuntimeMetrics(t *testing.T) {
	values, builds, err := parseRuntimeMetrics(`# TYPE process_resident_memory_bytes gauge
process_resident_memory_bytes 4096
# TYPE process_cpu_seconds_total counter
process_cpu_seconds_total 2.5
# TYPE go_gc_duration_seconds summary
go_gc_duration_seconds{quantile="0.5"} 0.01
go_gc_duration_seconds_sum 0.03
go_gc_duration_seconds_count 3
# TYPE devshard_build_info gauge
devshard_build_info{version="test",binary_version="before"} 1
loadtest_process_pid 17
loadtest_process_rss_hwm_bytes 8192
`)
	require.NoError(t, err)
	require.Equal(t, 4096.0, values["process_resident_memory_bytes"])
	require.Equal(t, 3.0, values["go_gc_duration_seconds_count"])
	require.Equal(t, 0.03, values["go_gc_duration_seconds_sum"])
	require.Equal(t, 17.0, values["loadtest_process_pid"])
	require.NotContains(t, values, "go_memstats_heap_alloc_bytes")
	require.Len(t, builds, 1)
	require.Equal(t, "before", builds[0]["binary_version"])
	_, _, err = parseRuntimeMetrics("not prometheus text")
	require.Error(t, err)
	_, _, err = parseRuntimeMetrics("some_unrelated_metric 1\n")
	require.Error(t, err)
}

func TestMetricsCountersDoNotCrossRestart(t *testing.T) {
	c := &metricsCollector{processes: map[string]*ProcessMetricsSummary{}, previous: map[string]map[string]float64{}, escrows: map[string]*EscrowMetricsSummary{}}
	sample := func(pid, start, cpu, rss float64) {
		c.observe(MetricsSample{Target: "host", Values: map[string]float64{"loadtest_process_pid": pid, "process_start_time_seconds": start, "process_cpu_seconds_total": cpu, "process_resident_memory_bytes": rss}})
	}
	sample(10, 100, 8, 1000)
	sample(10, 100, 10, 2000)
	sample(10, 200, 1, 500)
	sample(10, 200, 4, 750) // PID reuse.
	c.observe(MetricsSample{Target: "host", Errors: []string{"offline"}})
	s := c.processes["host"]
	require.Equal(t, 1, s.ProcessChanges)
	require.Equal(t, 5.0, s.Deltas["process_cpu_seconds_total"])
	require.Equal(t, 2000.0, s.Peaks["process_resident_memory_bytes"])
	require.Equal(t, 1000.0, s.First["process_resident_memory_bytes"])
	require.Equal(t, 750.0, s.Last["process_resident_memory_bytes"])
	require.Equal(t, 1, s.FailedSamples)
	require.NotContains(t, s.Deltas, "go_gc_duration_seconds_count")
}

func TestMetricsCollectorFinalSampleAndArtifacts(t *testing.T) {
	dir := t.TempDir()
	file, err := os.Create(filepath.Join(dir, "metrics.jsonl"))
	require.NoError(t, err)
	c := newMetricsCollector(context.Background(), file, dir, time.Second)
	nonce := uint64(800)
	c.collect = func(context.Context) []MetricsSample {
		return []MetricsSample{{Target: "escrow", EscrowID: "2", Nonce: &nonce, State: &GatewayStateSizes{Diffs: 256, DiffsBytes: 1024}}}
	}
	c.sample()
	close(c.done)
	summary := c.stop()
	require.Equal(t, summary, c.stop())
	require.Len(t, summary.Escrows, 1)
	require.Equal(t, 2, summary.Escrows[0].Samples)
	require.Equal(t, 256, summary.Escrows[0].MaxDiffs)
	require.Empty(t, summary.CollectionErrors)
	body, err := os.ReadFile(filepath.Join(dir, "metrics-summary.json"))
	require.NoError(t, err)
	var decoded MetricsSummary
	require.NoError(t, json.Unmarshal(body, &decoded))
	require.Equal(t, summary.Interval, decoded.Interval)
	stream, err := os.Open(filepath.Join(dir, "metrics.jsonl"))
	require.NoError(t, err)
	defer stream.Close()
	decoder := json.NewDecoder(stream)
	var first, last MetricsSample
	require.NoError(t, decoder.Decode(&first))
	require.NoError(t, decoder.Decode(&last))
	require.Equal(t, "baseline", first.Phase)
	require.Equal(t, "final", last.Phase)
}

func TestMetricsHTTPAuthAndStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			w.WriteHeader(401)
			return
		}
		_, _ = w.Write([]byte("go_goroutines 7\n"))
	}))
	defer server.Close()
	body, err := getMetricsHTTP(context.Background(), server.URL, "secret")
	require.NoError(t, err)
	require.Contains(t, body, "go_goroutines")
	_, err = getMetricsHTTP(context.Background(), server.URL, "")
	require.Error(t, err)
}

func TestLatencyWindowsDropsAndPartialMinute(t *testing.T) {
	scenario := testScenario()
	scenario.Workload.Duration = "90s"
	start := time.Unix(1000, 0)
	results := []RequestResult{
		{StartedAt: start, Duration: 10 * time.Millisecond, Outcome: "completed"},
		{StartedAt: start.Add(time.Second), Outcome: "dropped_by_generator"},
		{StartedAt: start.Add(61 * time.Second), Duration: 20 * time.Millisecond, Outcome: "completed"},
		{StartedAt: start.Add(62 * time.Second), Duration: 30 * time.Millisecond, Outcome: "error"},
	}
	windows := latencyWindows(scenario, start, results)
	require.Len(t, windows, 2)
	require.Equal(t, 1, windows[0].Dropped)
	require.Equal(t, 10*time.Millisecond, windows[0].P95)
	require.Equal(t, 30*time.Second, windows[1].Duration)
	require.Equal(t, 1, windows[1].Failed)
	require.InDelta(t, 1.0/30, windows[1].CompletedRPS, 0.000001)
}
