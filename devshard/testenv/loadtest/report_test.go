package loadtest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func writeReportFixture(t *testing.T, outputDir string, name string, value any) {
	t.Helper()
	body, err := json.Marshal(value)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(outputDir, name), body, 0o644))
}

func reportFixtureDir(t *testing.T) string {
	t.Helper()
	outputDir := t.TempDir()
	writeReportFixture(t, outputDir, "summary.json", Summary{Scenario: "prod-like-15m", Seed: 1515, Requests: 100, Completed: 80, Failed: 20, Dropped: 10, ErrorRate: 10.0 / 90, DroppedRate: 0.1, P50: 2 * time.Second, P95: 9 * time.Second})
	writeReportFixture(t, outputDir, "events.json", []EventRecord{
		{ID: "receipt-then-empty-stream", Case: "receipt-then-empty-stream", Action: "ml_fault", Target: "mock-openai-3", StartedAt: atPtr(time.Minute), EndedAt: atPtr(2 * time.Minute)},
		{ID: "timeout-vote-fanout", Case: "timeout-vote-fanout", Action: "ml_fault", Target: "mock-openai-4", StartedAt: atPtr(3 * time.Minute), EndedAt: atPtr(4 * time.Minute)},
		{ID: "gateway-killed-mid-race", Case: "gateway-killed-mid-race", Action: "gateway_restart", Target: "devshardctl", StartedAt: atPtr(3*time.Minute + 30*time.Second), EndedAt: atPtr(3*time.Minute + 32*time.Second)},
	})
	writeReportFixture(t, outputDir, "invariants.json", map[string]any{"tables": map[string]any{"error-rate-and-latency": []requestWindowStats{
		{Window: "baseline", Requests: 30},
		{Window: "quiet", Requests: 12, Errors: 0},
		{Window: "receipt-then-empty-stream", Requests: 40, Errors: 1, ErrorRate: 0.025, NonRetryable: 1},
		{Window: "timeout-vote-fanout", Requests: 20, Dropped: 4, Errors: 7, ErrorRate: 0.35, NonRetryable: 7},
		{Window: "timeout-vote-fanout+gateway-killed-mid-race", Requests: 9, Errors: 9, ErrorRate: 1},
	}}})
	writeReportFixture(t, outputDir, "assertions.json", map[string]any{"passed": false, "checks": []AssertionResult{
		{Name: "requests.error_rate", Passed: true, Expected: "<= 0.1200 (12.00%)", Actual: "0.1111 (11.11%)"},
		{Name: "requests.dropped_rate", Passed: true, Expected: "<= 0.1200 (12.00%)", Actual: "0.1000 (10.00%)"},
		{Name: "invariants.error-rate-and-latency.quiet_error_rate", Passed: true, Expected: "error rate <= 0.5%", Actual: "0.00% (0/12)"},
		{Name: "invariants.error-rate-and-latency.receipt-then-empty-stream.error_rate", Case: "receipt-then-empty-stream", Passed: false, Status: "XFAIL", Expected: "error rate <= 2%", Actual: "2.50% (1/40)"},
		{Name: "invariants.recovery-after-fault.receipt-then-empty-stream.recovery", Case: "receipt-then-empty-stream", Passed: true, Status: statusNotJudged, Expected: "first 30s bucket", Actual: "not judged: timeout-vote-fanout came 1m0s after the event"},
		{Name: "invariants.error-rate-and-latency.timeout-vote-fanout.error_rate", Case: "timeout-vote-fanout", Passed: false, Expected: "error rate <= 2%", Actual: "35.00% (7/20)"},
		{Name: "invariants.recovery-after-fault.timeout-vote-fanout.recovery", Case: "timeout-vote-fanout", Passed: false, Expected: "first 30s bucket", Actual: "never"},
		{Name: "invariants.error-rate-and-latency.gateway-killed-mid-race.error_rate", Case: "gateway-killed-mid-race", Passed: true, Status: statusNotJudged, Expected: "error rate <= 100%", Actual: "0.00% (0/0)"},
		{Name: "devshard.no_orphaned_work", Passed: true, Expected: "orphaned=0", Actual: "finished=90 orphaned=0 sealed=3 in_flight=1"},
		{Name: "invariants.escrow-accounting-reconciles.settle_count", Passed: true, Expected: "exactly one settle", Actual: "violations=0 escrows=4"},
	}})
	return outputDir
}

// Test flow:
// 1. Write a run directory with a summary, events, the error-rate-and-latency windows and checks of every status.
// 2. Render the report and require it written to report.md.
// 3. Require the verdict line to count every status, and each section to hold its rows in the template's order.
func TestWriteReportRendersEveryCheckInOneTemplate(t *testing.T) {
	outputDir := reportFixtureDir(t)

	report, err := WriteReport(outputDir)

	require.NoError(t, err)
	written, err := os.ReadFile(filepath.Join(outputDir, reportFileName))
	require.NoError(t, err)
	require.Equal(t, report, string(written))
	require.Contains(t, report, "# Load test report: prod-like-15m, seed 1515")
	require.Contains(t, report, "Verdict: FAIL. FIX: 2 · XPASS: 0 · INVALID: 0 · XFAIL: 1 · NOT_JUDGED: 2 · PASS: 5")
	require.Contains(t, report, "| gateway errors | 10 of 90 (11.11%) | <= 12.00% | PASS |")
	require.Contains(t, report, "| generator drops | 10 of 100 (10.00%) | <= 12.00% | PASS |")
	require.Contains(t, report, "| quiet window errors | 0.00% (0/12) | <= 0.5% | PASS |")
	require.Contains(t, report, "| timeout-vote-fanout | timeout-vote-fanout | 20 | 4 | 7 | 35.00% | 2% | 7 | FIX | never | FIX |")
	require.Contains(t, report, "| receipt-then-empty-stream | receipt-then-empty-stream | 40 | 0 | 1 | 2.50% | 2% | 1 | XFAIL | not judged: timeout-vote-fanout came 1m0s after the event | NOT_JUDGED |")
	require.Contains(t, report, "| gateway-killed-mid-race | gateway-killed-mid-race | 0 | 0 | 0 | 0.00% | 100% | 0 | NOT_JUDGED | — | — |")
	require.Contains(t, report, "| timeout-vote-fanout+gateway-killed-mid-race | 9 | 0 | 9 | 100.00% |")
	require.Contains(t, report, "| devshard.no_orphaned_work | PASS | orphaned=0 | finished=90 orphaned=0 sealed=3 in_flight=1 |")
	everyCheck := report[strings.Index(report, "## 5. Every check"):]
	require.Less(t, strings.Index(everyCheck, "invariants.recovery-after-fault.timeout-vote-fanout.recovery"), strings.Index(everyCheck, "invariants.error-rate-and-latency.receipt-then-empty-stream.error_rate"))
	require.Less(t, strings.Index(everyCheck, "invariants.error-rate-and-latency.receipt-then-empty-stream.error_rate"), strings.Index(everyCheck, "invariants.error-rate-and-latency.gateway-killed-mid-race.error_rate"))
	require.Less(t, strings.Index(everyCheck, "invariants.error-rate-and-latency.gateway-killed-mid-race.error_rate"), strings.Index(everyCheck, "requests.error_rate"))
}

// Test flow:
// 1. Write a run directory without assertions.json, as a run that failed before its checks.
// 2. Require WriteReport to return an error naming the missing file.
func TestWriteReportNeedsTheRunsChecks(t *testing.T) {
	_, err := WriteReport(t.TempDir())

	require.ErrorContains(t, err, "assertions.json")
}

// Test flow:
// 1. Write a run directory whose invariants.json has a harness_health invalid_fraction of 0.4626 and no INVALID check.
// 2. Render the report.
// 3. Require the line right under the verdict to give the stalled share, so INVALID: 0 cannot read as a clean run.
// 4. Render a run without harness_health and require the line to say the share was not recorded.
func TestWriteReportShowsTheStalledShareUnderTheVerdict(t *testing.T) {
	stalled := reportFixtureDir(t)
	writeReportFixture(t, stalled, "invariants.json", map[string]any{"harness_health": map[string]any{"invalid_fraction": 0.4626, "stalls": []harnessStall{{Source: "gateway metrics scrape"}, {Source: "gateway metrics scrape"}}}})
	unrecorded := reportFixtureDir(t)
	writeReportFixture(t, unrecorded, "invariants.json", map[string]any{"tables": map[string]any{}})

	stalledReport, err := WriteReport(stalled)
	require.NoError(t, err)
	unrecordedReport, err := WriteReport(unrecorded)
	require.NoError(t, err)

	require.Contains(t, stalledReport, "· PASS: 5\n\nStalled: 46.3% of the workload (harness_health.invalid_fraction, 2 stalls). INVALID counts only the checks whose every judged window overlaps a stall.\n")
	require.Contains(t, unrecordedReport, "· PASS: 5\n\nStalled: not recorded (invariants.json has no harness_health).\n")
}
