package loadtest

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// latencyClass is one max_tokens value's client latency over the requests the
// overall p50/p95 count: sent and timed, errors included. 0 is a request
// without max_tokens.
type latencyClass struct {
	MaxTokens    int64
	Requests     int
	Completed    int
	P50, P95     time.Duration
	P95Completed time.Duration
}

// runLatency splits the overall p95 by max_tokens: with a slow class near 5% of
// the mix the overall p95 sits on its boundary, and a few requests move it by
// the whole gap between classes.
type runLatency struct {
	P95Completed time.Duration
	Classes      []latencyClass
	Note         string
}

func loadRunLatency(outputDir string) runLatency {
	results, err := readLadderRequests(filepath.Join(outputDir, "requests.jsonl"))
	if err != nil {
		return runLatency{Note: "requests.jsonl is not readable (" + err.Error() + ")"}
	}
	completedOutcome := "completed"
	var scenario Scenario
	if err := loadYAML(filepath.Join(outputDir, "run.yaml"), &scenario); err == nil && scenario.Assertions.Requests.TerminalOutcome != "" {
		completedOutcome = scenario.Assertions.Requests.TerminalOutcome
	}
	return latencyByClass(results, completedOutcome)
}

func latencyByClass(results []RequestResult, completedOutcome string) runLatency {
	timed, completed := map[int64][]time.Duration{}, map[int64][]time.Duration{}
	var allCompleted []time.Duration
	for _, result := range results {
		if result.Outcome == "dropped_by_generator" || result.Duration <= 0 {
			continue
		}
		timed[result.MaxTokens] = append(timed[result.MaxTokens], result.Duration)
		if result.Outcome == completedOutcome {
			completed[result.MaxTokens] = append(completed[result.MaxTokens], result.Duration)
			allCompleted = append(allCompleted, result.Duration)
		}
	}
	sortDurations(allCompleted)
	latency := runLatency{P95Completed: percentile(allCompleted, 0.95)}
	for maxTokens, values := range timed {
		sortDurations(values)
		done := completed[maxTokens]
		sortDurations(done)
		latency.Classes = append(latency.Classes, latencyClass{
			MaxTokens: maxTokens, Requests: len(values), Completed: len(done),
			P50: percentile(values, 0.50), P95: percentile(values, 0.95), P95Completed: percentile(done, 0.95),
		})
	}
	sort.Slice(latency.Classes, func(a, b int) bool { return latency.Classes[a].MaxTokens < latency.Classes[b].MaxTokens })
	return latency
}

func sortDurations(values []time.Duration) {
	sort.Slice(values, func(a, b int) bool { return values[a] < values[b] })
}

func (r runReport) renderLatencyClasses(out *strings.Builder) {
	out.WriteString("\n### Latency by max_tokens\n\n")
	if r.latency.Note != "" {
		fmt.Fprintf(out, "Not recorded: %s.\n", r.latency.Note)
		return
	}
	out.WriteString("Over the same requests as the overall p50/p95, errors included; the last column counts completed requests only.\n\n| max_tokens | requests | completed | p50 | p95 | p95 of completed |\n| --- | --- | --- | --- | --- | --- |\n")
	for _, class := range r.latency.Classes {
		label := fmt.Sprint(class.MaxTokens)
		if class.MaxTokens == 0 {
			label = "unset"
		}
		writeReportRow(out, label, fmt.Sprint(class.Requests), fmt.Sprint(class.Completed), class.P50.String(), class.P95.String(), class.P95Completed.String())
	}
}
