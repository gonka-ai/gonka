package main

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"devshard/testenv/loadtest"
)

func printPerformanceReport(w io.Writer, summary loadtest.Summary, scenario loadtest.Scenario, passed bool, output, dataset string) {
	status := "FAIL"
	if passed {
		status = "PASS"
	}
	streamMode := fmt.Sprint(scenario.Workload.Request.Stream)
	if dataset != "" {
		streamMode = "per dataset request"
	}
	fmt.Fprintf(w, "\nRemote load test report\n=======================\nScenario: %s    Result: %s\nModel: %s    Streaming: %s    Concurrency: %d\nValidation: HTTP status only\n\n", summary.Scenario, status, scenario.Workload.Request.Model, streamMode, scenario.Workload.MaxInFlight)
	if dataset != "" {
		fmt.Fprintf(w, "Dataset: %s\nStreaming and max_tokens: per captured request\n\n", dataset)
	}
	fmt.Fprintln(w, "Client workload\n---------------")
	fmt.Fprintf(w, "  Request timeout:      %s (entire HTTP response)\n", scenario.RequestTimeout())
	fmt.Fprintf(w, "  Scheduled / completed: %d / %d\n  Failed requests:       %d\n  Generator drops:       %d\n  Error rate incl. drops: %.2f%% (allowed %.2f%%)\n  Workload / elapsed:    %s / %s\n", summary.Requests, summary.Completed, summary.Failed-summary.Dropped, summary.Dropped, summary.ErrorRate*100, scenario.Assertions.Requests.ErrorRate*100, scenario.Duration(), displayDuration(summary.Duration))
	rps := 0.0
	if summary.Duration > 0 {
		rps = float64(summary.Completed) / summary.Duration.Seconds()
	}
	fmt.Fprintf(w, "  Completed throughput:  %.2f req/s (including final responses)\n\n", rps)

	var latencies []time.Duration
	var total time.Duration
	statuses := map[int]int{}
	transportErrors := 0
	readErrors := 0
	for _, result := range summary.Results {
		if result.ResponseReadError != "" {
			readErrors++
		}
		if result.Outcome == "dropped_by_generator" {
			continue
		}
		if result.Status > 0 {
			statuses[result.Status]++
		} else {
			transportErrors++
		}
		if result.Duration > 0 {
			latencies = append(latencies, result.Duration)
			total += result.Duration
		}
	}
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	fmt.Fprintln(w, "Response latency\n----------------")
	if len(latencies) == 0 {
		fmt.Fprintln(w, "  No attempted request latencies available.")
	} else {
		quantile := func(p float64) time.Duration { return latencies[int(float64(len(latencies)-1)*p)] }
		fmt.Fprintf(w, "  min       mean      p50       p95       p99       max\n  %-9s %-9s %-9s %-9s %-9s %s\n", displayDuration(latencies[0]), displayDuration(total/time.Duration(len(latencies))), displayDuration(quantile(.5)), displayDuration(quantile(.95)), displayDuration(quantile(.99)), displayDuration(latencies[len(latencies)-1]))
	}
	fmt.Fprintln(w, "  Full response time, including failed attempts; drops excluded.\n  Streaming latency includes the entire stream, not time to first token.")
	if readErrors > 0 {
		fmt.Fprintf(w, "  Incomplete body reads: %d (latency ends at read error/timeout; HTTP status checks passed).\n", readErrors)
	}

	fmt.Fprintf(w, "\nPerformance windows (%s; grouped by request start)\n", summary.LatencyWindowInterval)
	fmt.Fprintln(w, "--------------------------------------------------------")
	peakRPS := 0.0
	for _, window := range summary.LatencyWindows {
		peakRPS = max(peakRPS, window.CompletedRPS)
	}
	table := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(table, "  Window\tOK\tFail\tDrop\tRPS\tp50\tp95\tp99\tThroughput")
	for _, window := range summary.LatencyWindows {
		offset := window.StartedAt.Sub(summary.StartedAt)
		bar := ""
		if peakRPS > 0 && window.CompletedRPS > 0 {
			bar = strings.Repeat("#", max(1, int(20*window.CompletedRPS/peakRPS)))
		}
		fmt.Fprintf(table, "  %s–%s\t%d\t%d\t%d\t%.2f\t%s\t%s\t%s\t%s\n", displayDuration(offset), displayDuration(offset+window.Duration), window.Completed, window.Failed, window.Dropped, window.CompletedRPS, displayDuration(window.P50), displayDuration(window.P95), displayDuration(window.P99), bar)
	}
	_ = table.Flush()
	fmt.Fprintln(w, "  Bars share one scale; RPS uses each window's workload duration.")

	fmt.Fprintln(w, "\nHTTP responses\n--------------")
	codes := make([]int, 0, len(statuses))
	for code := range statuses {
		codes = append(codes, code)
	}
	sort.Ints(codes)
	for _, code := range codes {
		fmt.Fprintf(w, "  HTTP %d: %d\n", code, statuses[code])
	}
	if transportErrors > 0 {
		fmt.Fprintf(w, "  No HTTP response: %d\n", transportErrors)
	}
	fmt.Fprintf(w, "\nArtifacts\n---------\n  %s\n  report.txt | summary.json | requests.jsonl | assertions.json | run.yaml\n", output)
}

func displayDuration(d time.Duration) string {
	return d.Round(time.Microsecond).String()
}
