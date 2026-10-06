// Binary loadtest runs one declarative Devshard load scenario in an isolated Docker stack.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"devshard/testenv/loadtest"
)

func main() {
	scenarioPath := flag.String("scenario", "", "path to a load scenario YAML")
	profilesDir := flag.String("profiles-dir", "", "directory containing ML profile YAML files")
	outputDir := flag.String("output", "", "directory for run artifacts")
	keepStack := flag.Bool("keep-stack", false, "keep the Docker stack and work directory after the run")
	loadDataset := flag.String("load-dataset", "", "JSONL dataset with captured client requests and ML responses")
	metricsInterval := flag.Duration("metrics-interval", time.Second, "process and escrow metrics sampling interval (minimum 100ms)")
	flag.Parse()
	if *scenarioPath == "" {
		log.Fatal("provide -scenario")
	}
	testenvDir, err := filepath.Abs(".")
	if err != nil {
		log.Fatal(err)
	}
	if *outputDir == "" {
		*outputDir = filepath.Join(testenvDir, "loadtest", "results", time.Now().UTC().Format("20060102T150405Z"))
	}
	if *loadDataset != "" {
		*loadDataset, err = filepath.Abs(*loadDataset)
		if err != nil {
			log.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	result, err := loadtest.RunScenario(ctx, loadtest.RunnerConfig{
		ScenarioPath:    *scenarioPath,
		ProfilesDir:     *profilesDir,
		TestenvDir:      testenvDir,
		OutputDir:       *outputDir,
		KeepStack:       *keepStack,
		LoadDataset:     *loadDataset,
		MetricsInterval: *metricsInterval,
	})
	if err != nil {
		if result.Summary.Requests > 0 {
			printSummary(result, false)
		}
		log.Fatal(err)
	}
	if result.Summary.Requests == 0 {
		fmt.Fprintf(os.Stdout, "Load test summary\n\nScenario: %s\nResult: NO REQUESTS\n\nArtifacts\n---------\n  output: %s\n", result.Summary.Scenario, result.OutputDir)
		return
	}
	printSummary(result, true)
}

func printSummary(result loadtest.RunResult, passed bool) {
	summary := result.Summary
	fmt.Fprintln(os.Stdout, "Load test summary")
	fmt.Fprintln(os.Stdout, "=================")
	fmt.Fprintln(os.Stdout)
	fmt.Fprintf(os.Stdout, "Scenario: %s\n", summary.Scenario)
	if passed {
		fmt.Fprintln(os.Stdout, "Result: PASS")
	} else {
		fmt.Fprintln(os.Stdout, "Result: FAIL")
	}
	fmt.Fprintln(os.Stdout)

	fmt.Fprintln(os.Stdout, "Client workload")
	fmt.Fprintln(os.Stdout, "---------------")
	fmt.Fprintf(os.Stdout, "  requests total:  %d\n", summary.Requests)
	fmt.Fprintf(os.Stdout, "  completed:       %d\n", summary.Completed)
	fmt.Fprintf(os.Stdout, "  failed:          %d\n", summary.Failed)
	fmt.Fprintf(os.Stdout, "  dropped:         %d\n", summary.Dropped)
	fmt.Fprintf(os.Stdout, "  error rate:      %.2f%%\n", summary.ErrorRate*100)
	fmt.Fprintln(os.Stdout)

	fmt.Fprintln(os.Stdout, "Latency")
	fmt.Fprintln(os.Stdout, "-------")
	fmt.Fprintf(os.Stdout, "  p50:              %s\n", summary.P50.String())
	fmt.Fprintf(os.Stdout, "  p95:              %s\n", summary.P95.String())
	fmt.Fprintf(os.Stdout, "  workload duration: %s\n", summary.Duration.String())
	if len(summary.LatencyWindows) > 0 {
		fmt.Fprintln(os.Stdout, "  Per-minute windows (requests grouped by start time)")
		fmt.Fprintln(os.Stdout, "    Window  Completed RPS  Failed Dropped       p50       p95       p99")
		for index, window := range summary.LatencyWindows {
			fmt.Fprintf(os.Stdout, "    %6d %14.2f %7d %7d %9s %9s %9s\n", index+1, window.CompletedRPS, window.Failed, window.Dropped, window.P50, window.P95, window.P99)
		}
	}
	fmt.Fprintln(os.Stdout)

	fmt.Fprintln(os.Stdout, "DevShard terminal state")
	fmt.Fprintln(os.Stdout, "-----------------------")
	if result.Terminal.Total > 0 || result.Terminal.Statuses != nil {
		terminal := result.Terminal
		fmt.Fprintf(os.Stdout, "  finished:         %d\n", terminal.Finished)
		fmt.Fprintf(os.Stdout, "  orphaned:         %d\n", terminal.Orphaned)
		fmt.Fprintf(os.Stdout, "  ghost:            %d\n", terminal.Ghost)
		fmt.Fprintf(os.Stdout, "  total:             %d\n", terminal.Total)
		fmt.Fprintf(os.Stdout, "  ghost rate:       %.2f%%\n", terminal.GhostRate*100)
		statuses := formatIntCounts(terminal.Statuses)
		if statuses == "" {
			statuses = "none"
		}
		fmt.Fprintf(os.Stdout, "  statuses:         %s\n", statuses)
	} else {
		fmt.Fprintln(os.Stdout, "  unavailable")
	}
	fmt.Fprintln(os.Stdout)

	printMLNodeStats(result.MLStats)
	fmt.Fprintln(os.Stdout)
	printGatewayStateSizes(result.GatewayState)
	fmt.Fprintln(os.Stdout)
	printMetricsSummary(result.Metrics, summary.Completed)
	fmt.Fprintln(os.Stdout)
	fmt.Fprintln(os.Stdout, "Artifacts")
	fmt.Fprintln(os.Stdout, "---------")
	fmt.Fprintf(os.Stdout, "  output: %s\n", result.OutputDir)
	fmt.Fprintln(os.Stdout)
	printAssertions(result.Assertions)
}

func printAssertions(assertions []loadtest.AssertionResult) {
	fmt.Fprintln(os.Stdout, "Assertions")
	fmt.Fprintln(os.Stdout, "----------")
	if len(assertions) == 0 {
		fmt.Fprintln(os.Stdout, "  no assertions evaluated")
		return
	}
	for _, assertion := range assertions {
		marker := "✗"
		if assertion.Passed {
			marker = "✓"
		}
		fmt.Fprintf(os.Stdout, "  %s %s\n", assertionMarker(marker, assertion.Passed), assertion.Name)
		fmt.Fprintf(os.Stdout, "      expected: %s\n", assertion.Expected)
		fmt.Fprintf(os.Stdout, "      actual:   %s\n", assertion.Actual)
		if assertion.Details != "" {
			fmt.Fprintf(os.Stdout, "      details:  %s\n", assertion.Details)
		}
	}
}

func assertionMarker(marker string, passed bool) string {
	info, err := os.Stdout.Stat()
	if err != nil || info.Mode()&os.ModeCharDevice == 0 {
		return marker
	}
	if passed {
		return "\033[32m" + marker + "\033[0m"
	}
	return "\033[31m" + marker + "\033[0m"
}

func printMLNodeStats(stats map[string]loadtest.MLNodeStats) {
	if len(stats) == 0 {
		return
	}
	keys := make([]string, 0, len(stats))
	for key := range stats {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	fmt.Fprintln(os.Stdout, "ML nodes")
	fmt.Fprintln(os.Stdout, "--------")
	fmt.Fprintln(os.Stdout, "  Node                 Allocated Received Successful Failed Timeouts Replay hits Replay misses Unaccounted Failure rate")
	for _, key := range keys {
		stat := stats[key]
		failureRate := 0.0
		if stat.RequestsReceived > 0 {
			failureRate = float64(stat.FailedResponses) / float64(stat.RequestsReceived)
		}
		accounted := stat.SuccessfulResponses + stat.FailedResponses + stat.Timeouts
		unaccounted := uint64(0)
		if stat.RequestsReceived > accounted {
			unaccounted = stat.RequestsReceived - accounted
		}
		fmt.Fprintf(os.Stdout, "  %-20s %9d %8d %10d %6d %8d %11d %13d %11d %11.2f%%\n", key, stat.Allocations, stat.RequestsReceived, stat.SuccessfulResponses, stat.FailedResponses, stat.Timeouts, stat.ReplayHits, stat.ReplayMisses, unaccounted, failureRate*100)
		if stat.Error != "" {
			fmt.Fprintf(os.Stdout, "    error: %s\n", stat.Error)
		}
	}
}

func printGatewayStateSizes(sizes loadtest.GatewayStateSizes) {
	fmt.Fprintln(os.Stdout, "Gateway state")
	fmt.Fprintln(os.Stdout, "-------------")
	fmt.Fprintf(os.Stdout, "  diff history:        %d\n", sizes.Diffs)
	fmt.Fprintf(os.Stdout, "  diff payload:        %.3f MiB (%d bytes)\n", sizes.DiffsMB, sizes.DiffsBytes)
	fmt.Fprintf(os.Stdout, "  signature nonces:    %d\n", sizes.SignatureNonces)
	fmt.Fprintf(os.Stdout, "  nonce states:        %d\n", sizes.NonceStates)
	fmt.Fprintf(os.Stdout, "  pending transactions: %d\n", sizes.PendingTxs)
	fmt.Fprintf(os.Stdout, "  applied tx keys:     %d\n", sizes.AppliedTxKeys)
}

func formatIntCounts(counts map[string]int) string {
	keys := make([]string, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", key, counts[key]))
	}
	return strings.Join(parts, ",")
}

func printMetricsSummary(summary loadtest.MetricsSummary, completed int) {
	fmt.Fprintln(os.Stdout, "Process memory and CPU")
	fmt.Fprintln(os.Stdout, "----------------------")
	if summary.Interval == "" {
		fmt.Fprintln(os.Stdout, "  unavailable")
		return
	}
	fmt.Fprintf(os.Stdout, "  interval: %s; RSS/heap peaks are sampled; HWM is process-lifetime RSS\n", summary.Interval)
	for _, process := range summary.Processes {
		fmt.Fprintf(os.Stdout, "  %s: samples=%d incomplete/failed=%d process changes=%d\n", process.Target, process.Samples, process.FailedSamples, process.ProcessChanges)
		for _, metric := range []struct{ name, label string }{
			{"process_resident_memory_bytes", "RSS"},
			{"go_memstats_heap_alloc_bytes", "heap allocated"},
			{"go_memstats_heap_inuse_bytes", "heap in use"},
		} {
			fmt.Fprintf(os.Stdout, "    %-15s baseline=%s final=%s workload peak=%s all-phase peak=%s\n", metric.label, metricMiB(process.First, metric.name), metricMiB(process.Last, metric.name), metricMiB(process.WorkloadPeaks, metric.name), metricMiB(process.Peaks, metric.name))
		}
		fmt.Fprintf(os.Stdout, "    RSS high-water mark: %s\n", metricMiB(process.Peaks, "loadtest_process_rss_hwm_bytes"))
		fmt.Fprintf(os.Stdout, "    observed CPU: %s s; allocated: %s; GC count: %s; GC pause: %s s\n", metricNumber(process.Deltas, "process_cpu_seconds_total"), metricMiB(process.Deltas, "go_memstats_alloc_bytes_total"), metricNumber(process.Deltas, "go_gc_duration_seconds_count"), metricNumber(process.Deltas, "go_gc_duration_seconds_sum"))
		if cpu, ok := process.Deltas["process_cpu_seconds_total"]; ok && completed > 0 {
			fmt.Fprintf(os.Stdout, "    CPU per 1000 completed client requests: %.3f s\n", cpu*1000/float64(completed))
		}
		fmt.Fprintf(os.Stdout, "    goroutines final=%s sampled peak=%s\n", metricNumber(process.Last, "go_goroutines"), metricNumber(process.Peaks, "go_goroutines"))
		if len(process.LastErrors) > 0 {
			fmt.Fprintf(os.Stdout, "    last collection errors: %s\n", strings.Join(process.LastErrors, "; "))
		}
	}
	fmt.Fprintln(os.Stdout, "  Counter deltas cover observed intervals within each process lifetime.")
	fmt.Fprintln(os.Stdout, "Escrow history during run")
	fmt.Fprintln(os.Stdout, "-------------------------")
	for _, escrow := range summary.Escrows {
		first, last := "unavailable", "unavailable"
		if escrow.FirstNonce != nil {
			first = fmt.Sprint(*escrow.FirstNonce)
		}
		if escrow.LastNonce != nil {
			last = fmt.Sprint(*escrow.LastNonce)
		}
		fmt.Fprintf(os.Stdout, "  escrow=%s nonce=%s -> %s samples=%d max retained diffs=%d payload=%.3f MiB max signature nonces=%d\n", escrow.EscrowID, first, last, escrow.Samples, escrow.MaxDiffs, float64(escrow.MaxDiffBytes)/(1<<20), escrow.MaxSignatureNonces)
		if escrow.First != nil && escrow.Last != nil {
			fmt.Fprintf(os.Stdout, "    diffs=%d -> %d signatures=%d -> %d nonce states=%d -> %d applied tx keys=%d -> %d pending txs=%d -> %d\n", escrow.First.Diffs, escrow.Last.Diffs, escrow.First.SignatureNonces, escrow.Last.SignatureNonces, escrow.First.NonceStates, escrow.Last.NonceStates, escrow.First.AppliedTxKeys, escrow.Last.AppliedTxKeys, escrow.First.PendingTxs, escrow.Last.PendingTxs)
		}
	}
	if len(summary.CollectionErrors) > 0 {
		fmt.Fprintf(os.Stdout, "  collection/artifact errors: %d (see metrics-summary.json)\n", len(summary.CollectionErrors))
	}
	fmt.Fprintln(os.Stdout, "  artifacts: metrics.jsonl, metrics-summary.json")
}

func metricMiB(values map[string]float64, name string) string {
	if value, ok := values[name]; ok {
		return fmt.Sprintf("%.3f MiB", value/(1<<20))
	}
	return "unavailable"
}
func metricNumber(values map[string]float64, name string) string {
	if value, ok := values[name]; ok {
		return fmt.Sprintf("%.3f", value)
	}
	return "unavailable"
}
