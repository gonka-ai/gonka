package loadtest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

const runSummaryFileName = "run-summary.json"

// RunSummary is run-summary.json: one flat record per run with the same fields for either gateway,
// so runs of one scenario on both drivers can be tabulated side by side.
type RunSummary struct {
	Scenario        string                    `json:"scenario"`
	Gateway         string                    `json:"gateway"`
	Run             string                    `json:"run"`
	Seed            int64                     `json:"seed"`
	StartedAt       time.Time                 `json:"started_at"`
	WorkloadSeconds float64                   `json:"workload_seconds"`
	Verdict         string                    `json:"verdict"`
	StatusCounts    map[string]int            `json:"status_counts"`
	Requests        RunSummaryRequests        `json:"requests"`
	LatencySeconds  RunSummaryLatency         `json:"latency_seconds"`
	Inferences      map[string]int            `json:"inference_statuses"`
	Burns           int                       `json:"burned_nonces"`
	Processes       map[string]ProcessSummary `json:"processes"`
	FailedChecks    []string                  `json:"failed_checks"`
	NotApplicable   []string                  `json:"not_applicable_checks"`
	Spend           RunSummarySpend           `json:"spend"`
	// ChallengedAtSeal is nil when the run saved no drain read or no run.yaml to judge it by.
	ChallengedAtSeal *RunSummaryChallengedAtSeal `json:"challenged_at_seal,omitempty"`
}

// RunSummaryChallengedAtSeal counts the records devshard.no_orphaned_work's last read found still challenged on an
// escrow settled on chain or parked with settlement off, in total and per escrow.
type RunSummaryChallengedAtSeal struct {
	Records  int            `json:"records"`
	ByEscrow map[string]int `json:"by_escrow"`
}

// RunSummaryRequests counts the client side of a run.
type RunSummaryRequests struct {
	Planned      int     `json:"planned"`
	Sent         int     `json:"sent"`
	Completed    int     `json:"completed"`
	Failed       int     `json:"failed"`
	Dropped      int     `json:"dropped"`
	ErrorRate    float64 `json:"error_rate"`
	DroppedRate  float64 `json:"dropped_rate"`
	CompletedRPS float64 `json:"completed_rps"`
}

// RunSummaryLatency holds the client latency quantiles of every request sent and timed, errors included, with
// the p95 of completed requests alone and each max_tokens class's own figures. See runLatency.
type RunSummaryLatency struct {
	P50          float64                  `json:"p50"`
	P95          float64                  `json:"p95"`
	P95Completed float64                  `json:"p95_completed"`
	Classes      []RunSummaryLatencyClass `json:"by_max_tokens"`
}

// RunSummaryLatencyClass is one max_tokens value's latency; 0 is a request without max_tokens.
type RunSummaryLatencyClass struct {
	MaxTokens    int64   `json:"max_tokens"`
	Requests     int     `json:"requests"`
	Completed    int     `json:"completed"`
	P50          float64 `json:"p50"`
	P95          float64 `json:"p95"`
	P95Completed float64 `json:"p95_completed"`
}

// ProcessSummary is one sampled process's cost over the run.
type ProcessSummary struct {
	CPUSeconds     float64 `json:"cpu_seconds"`
	PeakRSSBytes   float64 `json:"peak_rss_bytes"`
	PeakHeapBytes  float64 `json:"peak_heap_inuse_bytes"`
	PeakGoroutines float64 `json:"peak_goroutines"`
}

// writeRunSummary builds run-summary.json from the run's artifacts; artifacts a run did not write leave their fields empty.
func writeRunSummary(outputDir string, run runReport) (RunSummary, error) {
	summary := RunSummary{
		Scenario:        run.summary.Scenario,
		Gateway:         gatewayDriverName(run.inputs.Gateway),
		Run:             filepath.Base(outputDir),
		Seed:            run.summary.Seed,
		StartedAt:       run.summary.StartedAt,
		WorkloadSeconds: run.summary.Duration.Seconds(),
		Verdict:         run.verdict(),
		StatusCounts:    map[string]int{},
		Inferences:      map[string]int{},
		Burns:           len(run.inputs.Burns),
		Processes:       map[string]ProcessSummary{},
		FailedChecks:    []string{},
		NotApplicable:   []string{},
	}
	sent := run.summary.Requests - run.summary.Dropped
	summary.Requests = RunSummaryRequests{Planned: run.summary.Requests, Sent: sent, Completed: run.summary.Completed, Failed: run.summary.Failed, Dropped: run.summary.Dropped, ErrorRate: run.summary.ErrorRate, DroppedRate: run.summary.DroppedRate}
	if run.summary.Duration > 0 {
		summary.Requests.CompletedRPS = float64(run.summary.Completed) / run.summary.Duration.Seconds()
	}
	summary.LatencySeconds = RunSummaryLatency{P50: run.summary.P50.Seconds(), P95: run.summary.P95.Seconds(), P95Completed: run.latency.P95Completed.Seconds(), Classes: []RunSummaryLatencyClass{}}
	for _, class := range run.latency.Classes {
		summary.LatencySeconds.Classes = append(summary.LatencySeconds.Classes, RunSummaryLatencyClass{
			MaxTokens: class.MaxTokens, Requests: class.Requests, Completed: class.Completed,
			P50: class.P50.Seconds(), P95: class.P95.Seconds(), P95Completed: class.P95Completed.Seconds(),
		})
	}
	for _, check := range run.checks {
		status := reportStatus(check)
		summary.StatusCounts[status]++
		if status == statusFix {
			summary.FailedChecks = append(summary.FailedChecks, check.Name)
		}
		if strings.HasPrefix(check.Details, "not applicable to ") {
			summary.NotApplicable = append(summary.NotApplicable, check.Name)
		}
	}
	if run.inputs.Orphans != nil && run.inputs.Orphans.Last != nil {
		for _, runtime := range run.inputs.Orphans.Last.Runtimes {
			for _, inference := range runtime.Inferences {
				summary.Inferences[inference.Status]++
			}
		}
	}
	if run.scenario != nil && run.inputs.Orphans != nil && run.inputs.Orphans.Last != nil {
		terminal, _, _ := judgeOrphans(*run.inputs.Orphans, orphanJudgeFor(*run.scenario, run.inputs, run.events))
		summary.ChallengedAtSeal = &RunSummaryChallengedAtSeal{Records: terminal.ChallengedAtSeal, ByEscrow: terminal.ChallengedAtSealByEscrow}
		if summary.ChallengedAtSeal.ByEscrow == nil {
			summary.ChallengedAtSeal.ByEscrow = map[string]int{}
		}
	}
	ledger, inferences, ledgerErr := spendSources(run.inputs)
	summary.Spend = buildSpend(ledger, inferences, readNonces(readSamples[PendingSample](outputDir, "pending.jsonl"), run.inputs.Invariants), ledgerErr, run.summary.Completed)
	var metrics MetricsSummary
	if err := readReportArtifact(outputDir, "metrics-summary.json", false, &metrics); err != nil {
		return summary, err
	}
	for _, process := range metrics.Processes {
		summary.Processes[process.Target] = ProcessSummary{
			CPUSeconds:     process.Deltas["process_cpu_seconds_total"],
			PeakRSSBytes:   process.Peaks["process_resident_memory_bytes"],
			PeakHeapBytes:  process.Peaks["go_memstats_heap_inuse_bytes"],
			PeakGoroutines: process.Peaks["go_goroutines"],
		}
	}
	slices.Sort(summary.FailedChecks)
	slices.Sort(summary.NotApplicable)
	body, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		return summary, err
	}
	return summary, os.WriteFile(filepath.Join(outputDir, runSummaryFileName), append(body, '\n'), 0o644)
}
