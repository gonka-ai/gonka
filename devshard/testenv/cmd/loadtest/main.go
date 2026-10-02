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
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	result, err := loadtest.RunScenario(ctx, loadtest.RunnerConfig{
		ScenarioPath: *scenarioPath,
		ProfilesDir:  *profilesDir,
		TestenvDir:   testenvDir,
		OutputDir:    *outputDir,
		KeepStack:    *keepStack,
	})
	if err != nil {
		log.Fatal(err)
	}
	if result.Summary.Requests == 0 {
		fmt.Fprintf(os.Stdout, "Load test summary\n  scenario: %s\n  output: %s\n", result.Summary.Scenario, result.OutputDir)
		return
	}
	printSummary(result)
}

func printSummary(result loadtest.RunResult) {
	summary := result.Summary
	fmt.Fprintln(os.Stdout, "Load test summary")
	fmt.Fprintf(os.Stdout, "  scenario: %s\n", summary.Scenario)
	fmt.Fprintf(os.Stdout, "  requests: total=%d completed=%d failed=%d dropped=%d\n", summary.Requests, summary.Completed, summary.Failed, summary.Dropped)
	fmt.Fprintf(os.Stdout, "  client error rate: %.2f%%\n", summary.ErrorRate*100)
	fmt.Fprintf(os.Stdout, "  latency: p50=%s p95=%s workload=%s\n", summary.P50.String(), summary.P95.String(), summary.Duration.String())
	if result.Terminal.Total > 0 {
		terminal := result.Terminal
		fmt.Fprintf(os.Stdout, "  terminal state: finished=%d ghost=%d total=%d ghost rate=%.2f%% statuses=%s\n", terminal.Finished, terminal.Ghost, terminal.Total, terminal.GhostRate*100, formatIntCounts(terminal.Statuses))
	} else {
		fmt.Fprintln(os.Stdout, "  terminal state: unavailable")
	}
	if len(result.Allocations) > 0 {
		fmt.Fprintf(os.Stdout, "  allocations: %s\n", formatUintCounts(result.Allocations))
	}
	printMLNodeStats(result.MLStats)
	printGatewayStateSizes(result.GatewayState)
	fmt.Fprintf(os.Stdout, "  output: %s\n", result.OutputDir)
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
	fmt.Fprintln(os.Stdout, "  ML node stats:")
	for _, key := range keys {
		stat := stats[key]
		failureRate := 0.0
		if stat.RequestsReceived > 0 {
			failureRate = float64(stat.FailedResponses) / float64(stat.RequestsReceived)
		}
		line := fmt.Sprintf("    %s: allocations=%d requests=%d successful=%d failed=%d timeouts=%d failure_rate=%.2f%%", key, stat.Allocations, stat.RequestsReceived, stat.SuccessfulResponses, stat.FailedResponses, stat.Timeouts, failureRate*100)
		if stat.Error != "" {
			line += " error=" + stat.Error
		}
		fmt.Fprintln(os.Stdout, line)
	}
}

func printGatewayStateSizes(sizes loadtest.GatewayStateSizes) {
	fmt.Fprintf(os.Stdout, "  gateway state: diffs=%d size=%.3fMB signature_nonces=%d nonce_states=%d pending_txs=%d applied_tx_keys=%d\n", sizes.Diffs, sizes.DiffsMB, sizes.SignatureNonces, sizes.NonceStates, sizes.PendingTxs, sizes.AppliedTxKeys)
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

func formatUintCounts(counts map[string]uint64) string {
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
