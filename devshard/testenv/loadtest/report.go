package loadtest

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

const (
	reportFileName = "report.md"
	statusFix      = "FIX"
	statusPass     = "PASS"
	reportMissing  = "—"
)

// reportStatusOrder is the order of the verdict counts and of the every-check table.
var reportStatusOrder = []string{statusFix, "XPASS", statusInvalid, "XFAIL", statusNotJudged, statusPass}

var endStatePrefixes = []string{"devshard.", "escrow_rotation.", "invariants.escrow-accounting-reconciles.", "invariants.no-nonce-left-pending.", "invariants.no-runtime-for-deactivated-escrow."}

type runReport struct {
	summary Summary
	passed  bool
	checks  []AssertionResult
	events  []EventRecord
	windows []requestWindowStats
	ladder  ladderReport
	health  *harnessHealth
	build   *BuildRecord
}

// WriteReport renders the run's fixed-template report from its artifacts
// into report.md and returns it; it reads only files the run wrote.
func WriteReport(outputDir string) (string, error) {
	run, err := loadRunReport(outputDir)
	if err != nil {
		return "", err
	}
	report := run.render(filepath.Base(outputDir))
	if err := os.WriteFile(filepath.Join(outputDir, reportFileName), []byte(report), 0o644); err != nil {
		return "", err
	}
	return report, nil
}

func loadRunReport(outputDir string) (runReport, error) {
	var run runReport
	var assertions struct {
		Passed bool              `json:"passed"`
		Checks []AssertionResult `json:"checks"`
	}
	if err := readReportArtifact(outputDir, "assertions.json", true, &assertions); err != nil {
		return run, err
	}
	run.passed, run.checks = assertions.Passed, assertions.Checks
	if err := readReportArtifact(outputDir, "summary.json", false, &run.summary); err != nil {
		return run, err
	}
	if err := readReportArtifact(outputDir, buildFileName, false, &run.build); err != nil {
		return run, err
	}
	if err := readReportArtifact(outputDir, "events.json", false, &run.events); err != nil {
		return run, err
	}
	var invariants struct {
		Tables struct {
			ErrorRateAndLatency []requestWindowStats `json:"error-rate-and-latency"`
		} `json:"tables"`
		Health *harnessHealth `json:"harness_health"`
	}
	if err := readReportArtifact(outputDir, "invariants.json", false, &invariants); err != nil {
		return run, err
	}
	run.windows, run.health = invariants.Tables.ErrorRateAndLatency, invariants.Health
	run.ladder = loadLadder(outputDir, run.summary)
	return run, nil
}

func readReportArtifact(outputDir, name string, required bool, out any) error {
	body, err := os.ReadFile(filepath.Join(outputDir, name))
	if os.IsNotExist(err) && !required {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read %s: %w", name, err)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode %s: %w", name, err)
	}
	return nil
}

func reportStatus(check AssertionResult) string {
	switch {
	case check.Status != "":
		return check.Status
	case check.Passed:
		return statusPass
	default:
		return statusFix
	}
}

func (r runReport) check(name string) (AssertionResult, bool) {
	for _, check := range r.checks {
		if check.Name == name {
			return check, true
		}
	}
	return AssertionResult{}, false
}

func (r runReport) window(name string) requestWindowStats {
	for _, window := range r.windows {
		if window.Window == name {
			return window
		}
	}
	return requestWindowStats{Window: name}
}

func (r runReport) checkCells(name string) (status, expected, actual string) {
	check, found := r.check(name)
	if !found {
		return reportMissing, reportMissing, reportMissing
	}
	return reportStatus(check), check.Expected, check.Actual
}

func (r runReport) render(runName string) string {
	var out strings.Builder
	verdict := "PASS"
	if !r.passed {
		verdict = "FAIL"
	}
	counts := map[string]int{}
	for _, check := range r.checks {
		counts[reportStatus(check)]++
	}
	countCells := make([]string, 0, len(reportStatusOrder))
	for _, status := range reportStatusOrder {
		countCells = append(countCells, fmt.Sprintf("%s: %d", status, counts[status]))
	}
	fmt.Fprintf(&out, "# Load test report: %s, seed %d, %s\n\n", r.summary.Scenario, r.summary.Seed, runName)
	if r.build != nil {
		fmt.Fprintf(&out, "Build: %s, protocol %s, binary %s, devshardd sha256 %s.\n\n", r.build.Name, r.build.Protocol, r.build.BinaryVersion, r.build.DevsharddSHA256)
	}
	fmt.Fprintf(&out, "Verdict: %s. %s\n\n", verdict, strings.Join(countCells, " · "))
	r.renderStalledShare(&out)
	out.WriteString("FIX: a bound was broken and needs a fix. XPASS: a known bug no longer reproduces; drop its marker. INVALID: a harness stall covers the check. XFAIL: a known bug failed as expected. NOT_JUDGED: the run left no data for the check. PASS: the bound held.\n")
	r.renderTraffic(&out)
	r.renderLadder(&out)
	r.renderFaultWindows(&out)
	r.renderOverlaps(&out)
	r.renderEndState(&out)
	r.renderEveryCheck(&out)
	return out.String()
}

// renderStalledShare puts the run's stalled share under the verdict: only
// fault-window checks can turn INVALID, so the counts alone hide a void run.
func (r runReport) renderStalledShare(out *strings.Builder) {
	if r.health == nil {
		out.WriteString("Stalled: not recorded (invariants.json has no harness_health).\n\n")
		return
	}
	fmt.Fprintf(out, "Stalled: %s of the workload (harness_health.invalid_fraction, %d stalls). INVALID counts only the checks whose every judged window overlaps a stall.\n\n", formatInvalidFraction(r.health.InvalidFraction), len(r.health.Stalls))
}

func (r runReport) renderTraffic(out *strings.Builder) {
	sent := r.summary.Requests - r.summary.Dropped
	errorsStatus, errorsBound, _ := r.checkCells("requests.error_rate")
	droppedStatus, droppedBound, _ := r.checkCells("requests.dropped_rate")
	quietErrorsStatus, quietErrorsBound, quietErrors := r.checkCells("invariants.error-rate-and-latency.quiet_error_rate")
	quietLatencyStatus, quietLatencyBound, quietLatency := r.checkCells("invariants.error-rate-and-latency.quiet_latency")
	out.WriteString("\n## 1. Client traffic\n\n| metric | value | bound | status |\n| --- | --- | --- | --- |\n")
	writeReportRow(out, "planned arrivals", fmt.Sprint(r.summary.Requests), "", "")
	writeReportRow(out, "offered", fmt.Sprint(r.summary.Requests-r.summary.Behind), "", "")
	writeReportRow(out, "sent to gateway", fmt.Sprint(sent), "", "")
	writeReportRow(out, "gateway errors", shareOf(sent-r.summary.Completed, sent), percentBound(errorsBound), errorsStatus)
	writeReportRow(out, "generator drops", shareOf(r.summary.Dropped, r.summary.Requests), percentBound(droppedBound), droppedStatus)
	writeReportRow(out, "of them generator behind", shareOf(r.summary.Behind, r.summary.Requests), "", "")
	writeReportRow(out, "latency p50 / p95", fmt.Sprintf("%s / %s", r.summary.P50, r.summary.P95), "", "")
	writeReportRow(out, "quiet window errors", quietErrors, strings.TrimPrefix(quietErrorsBound, "error rate "), quietErrorsStatus)
	writeReportRow(out, "quiet window latency", quietLatency, quietLatencyBound, quietLatencyStatus)
}

func (r runReport) renderFaultWindows(out *strings.Builder) {
	out.WriteString("\n## 2. Fault windows\n\nEach fault is judged over the requests only it touched; recovery is judged before the next fault starts.\n\n")
	out.WriteString("| event | case | requests | dropped | errors | rate | bound | non-503/429 | errors status | recovery | recovery status |\n| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |\n")
	for _, event := range r.events {
		if isBackground(event.Params) {
			continue
		}
		window := r.window(event.ID)
		errorsStatus, errorsBound, _ := r.checkCells("invariants.error-rate-and-latency." + event.ID + ".error_rate")
		recoveryStatus, _, recovery := r.checkCells("invariants.recovery-after-fault." + event.ID + ".recovery")
		writeReportRow(out, event.ID, event.Case, fmt.Sprint(window.Requests), fmt.Sprint(window.Dropped), fmt.Sprint(window.Errors), fmt.Sprintf("%.2f%%", window.ErrorRate*100), strings.TrimPrefix(errorsBound, "error rate <= "), fmt.Sprint(window.NonRetryable), errorsStatus, recovery, recoveryStatus)
	}
}

func (r runReport) renderOverlaps(out *strings.Builder) {
	out.WriteString("\n## 3. Overlaps\n\nRequests several faults touched at once; shown, judged by none.\n\n| window | requests | dropped | errors | rate |\n| --- | --- | --- | --- | --- |\n")
	for _, window := range r.windows {
		if strings.Contains(window.Window, "+") {
			writeReportRow(out, window.Window, fmt.Sprint(window.Requests), fmt.Sprint(window.Dropped), fmt.Sprint(window.Errors), fmt.Sprintf("%.2f%%", window.ErrorRate*100))
		}
	}
}

func (r runReport) renderEndState(out *strings.Builder) {
	out.WriteString("\n## 4. End state\n\n| check | status | expected | actual |\n| --- | --- | --- | --- |\n")
	for _, check := range r.checks {
		if slices.ContainsFunc(endStatePrefixes, func(prefix string) bool { return strings.HasPrefix(check.Name, prefix) }) {
			writeReportRow(out, check.Name, reportStatus(check), check.Expected, check.Actual)
		}
	}
}

func (r runReport) renderEveryCheck(out *strings.Builder) {
	out.WriteString("\n## 5. Every check\n\n| check | case | status | expected | actual | details |\n| --- | --- | --- | --- | --- | --- |\n")
	ordered := slices.Clone(r.checks)
	slices.SortStableFunc(ordered, func(left, right AssertionResult) int {
		return slices.Index(reportStatusOrder, reportStatus(left)) - slices.Index(reportStatusOrder, reportStatus(right))
	})
	for _, check := range ordered {
		writeReportRow(out, check.Name, check.Case, reportStatus(check), check.Expected, check.Actual, check.Details)
	}
}

func shareOf(part, whole int) string {
	if whole == 0 {
		return fmt.Sprintf("%d of 0", part)
	}
	return fmt.Sprintf("%d of %d (%.2f%%)", part, whole, float64(part)/float64(whole)*100)
}

// percentBound turns an assertion bound like "<= 0.1200 (12.00%)" into "<= 12.00%".
func percentBound(expected string) string {
	open, closing := strings.Index(expected, "("), strings.LastIndex(expected, ")")
	if open < 0 || closing < open {
		return expected
	}
	return "<= " + expected[open+1:closing]
}

func writeReportRow(out *strings.Builder, cells ...string) {
	escaped := make([]string, len(cells))
	for i, cell := range cells {
		escaped[i] = strings.ReplaceAll(strings.ReplaceAll(cell, "|", `\|`), "\n", " ")
	}
	fmt.Fprintf(out, "| %s |\n", strings.Join(escaped, " | "))
}
