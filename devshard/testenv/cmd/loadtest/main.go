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
	fmt.Fprintln(os.Stdout)

	fmt.Fprintln(os.Stdout, "DevShard terminal state")
	fmt.Fprintln(os.Stdout, "-----------------------")
	if result.Terminal.Total > 0 {
		terminal := result.Terminal
		fmt.Fprintf(os.Stdout, "  finished:         %d\n", terminal.Finished)
		fmt.Fprintf(os.Stdout, "  ghost:            %d\n", terminal.Ghost)
		fmt.Fprintf(os.Stdout, "  total:             %d\n", terminal.Total)
		fmt.Fprintf(os.Stdout, "  ghost rate:       %.2f%%\n", terminal.GhostRate*100)
		fmt.Fprintf(os.Stdout, "  statuses:         %s\n", formatIntCounts(terminal.Statuses))
	} else {
		fmt.Fprintln(os.Stdout, "  unavailable")
	}
	fmt.Fprintln(os.Stdout)

	printMLNodeStats(result.MLStats)
	fmt.Fprintln(os.Stdout)
	printGatewayStateSizes(result.GatewayState)
	fmt.Fprintln(os.Stdout)
	fmt.Fprintln(os.Stdout, "Artifacts")
	fmt.Fprintln(os.Stdout, "---------")
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
	fmt.Fprintln(os.Stdout, "ML nodes")
	fmt.Fprintln(os.Stdout, "--------")
	fmt.Fprintln(os.Stdout, "  Node                 Allocated Received Successful Failed Timeouts Unaccounted Failure rate")
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
		fmt.Fprintf(os.Stdout, "  %-20s %9d %8d %10d %6d %8d %11d %11.2f%%\n", key, stat.Allocations, stat.RequestsReceived, stat.SuccessfulResponses, stat.FailedResponses, stat.Timeouts, unaccounted, failureRate*100)
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
