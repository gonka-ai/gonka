package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/testenv/citest/harness"
)

// writeBaselineRun writes one run directory of a baseline as make loadtest-baseline leaves it: the
// exit code, summary.json, assertions.json and, when rep is set, promises.json.
func writeBaselineRun(t *testing.T, dir, name, exit string, requests, failed int, failedChecks []string, rep *harness.PromiseReport) {
	t.Helper()
	run := filepath.Join(dir, name)
	require.NoError(t, os.MkdirAll(run, 0o755))
	write := func(file string, v any) {
		raw, err := json.Marshal(v)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(run, file), raw, 0o644))
	}
	require.NoError(t, os.WriteFile(filepath.Join(run, "exit-code"), []byte(exit+"\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(run, "run.yaml"), []byte("scenario: "+name+"\n"), 0o644))
	write("summary.json", map[string]any{"requests": requests, "failed": failed})
	checks := []map[string]any{{"name": "requests.error_rate", "passed": true}}
	for _, c := range failedChecks {
		checks = append(checks, map[string]any{"name": c, "passed": false})
	}
	write("assertions.json", map[string]any{"passed": len(failedChecks) == 0, "checks": checks})
	if rep != nil {
		write("promises.json", rep)
	}
}

func leakyReport() *harness.PromiseReport {
	rep := harness.CheckPromises(harness.PromiseInput{})
	rep.Checked[harness.NodeLocksReleased] = harness.Measure{Measured: true, Judged: 3}
	rep.Violations = []harness.PromiseViolation{{Promise: harness.NodeLocksReleased, Host: "n4", Detail: "1 of 3 locks on n4 never released"}}
	return &rep
}

// heldReport is a run on which every check was measured and held.
func heldReport() *harness.PromiseReport {
	rep := harness.CheckPromises(harness.PromiseInput{})
	for _, c := range harness.PromiseCatalog {
		rep.Checked[c.Name] = harness.Measure{Measured: true, Judged: 3}
	}
	return &rep
}

// Test flow:
//  1. A report whose checks held on two, were only observed on one and were not measured on the rest.
//  2. Take its promise verdict, then the same report marked not valid.
//  3. Check the verdict counts each kind instead of saying held, and the not-valid one says so first.
func TestPromisecheckBaselineVerdictCountsTheChecksNotMeasured(t *testing.T) {
	rep := harness.PromiseReport{Checked: map[string]harness.Measure{}}
	for _, c := range harness.PromiseCatalog {
		rep.Checked[c.Name] = harness.Measure{Note: "not measured: no input"}
	}
	rep.Checked[harness.NodeLocksReleased] = harness.Measure{Measured: true, Judged: 3}
	rep.Checked[harness.EscrowMoneyConserved] = harness.Measure{Measured: true, Judged: 4}
	rep.Checked[harness.GatewayHeapBounded] = harness.Measure{Note: "counted, no bound set"}
	run := baselineRun{Name: "drain", Report: &rep}
	want := fmt.Sprintf("held 2, observed 1, not measured %d", len(harness.PromiseCatalog)-3)
	require.Equal(t, want, run.promisesCell())

	rep.Invalid = []string{"fault rule r1 (n1) never fired"}
	require.Equal(t, "not valid; "+want, run.promisesCell())
}

// Test flow:
//  1. Two baselines of one scenario that is not valid on both sides (a fault rule never fired), whose
//     money check held before and was violated after.
//  2. Compare them.
//  3. Check the money cell keeps its verdict marked as on a run not valid, changes from ok to 1 in
//     bold, the promise verdict names the violation, and the change is counted.
func TestPromisecheckCompareShowsAViolationOnARunNotValid(t *testing.T) {
	report := func(violated bool) *harness.PromiseReport {
		rep := heldReport()
		rep.Invalid = []string{"fault rule r1 (n1) never fired"}
		if violated {
			rep.Violations = []harness.PromiseViolation{{Promise: harness.EscrowMoneyConserved, Host: "versiond-2", Detail: "books off by 7"}}
		}
		return rep
	}
	before, after := t.TempDir(), t.TempDir()
	writeBaselineRun(t, before, "phase-faults", "1", 100, 0, []string{"promises"}, report(false))
	writeBaselineRun(t, after, "phase-faults", "1", 100, 0, []string{"promises"}, report(true))

	code, out := runPromisecheckOutput(t, "-compare", before, after)
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "| **not valid; held → not valid; violated: escrow_money_conserved** |")
	require.Contains(t, out, " **ok (run not valid) → 1 (run not valid)** |")
	require.Contains(t, out, " ok (run not valid) |", "an unchanged check of a run not valid keeps its verdict")
	require.Contains(t, out, "\n2 changed cells.\n")
}

// Test flow:
//  1. A baseline of three runs: one that held, one whose locks leaked and whose runner check failed,
//     and one that crashed before it wrote anything but its exit code; plus a stray file.
//  2. Summarize the directory.
//  3. Check a row per run with its exit code, counts, failed runner checks and promise verdict, the
//     crashed run shown as having no report, and the promise table over the two reports.
func TestPromisecheckSummarizesABaselineDirectory(t *testing.T) {
	dir := t.TempDir()
	writeBaselineRun(t, dir, "drain", "0", 120, 0, nil, heldReport())
	writeBaselineRun(t, dir, "vote-fanout", "1", 900, 3, []string{"invariants.no-nonce-left-pending"}, leakyReport())
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "spike-load"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "spike-load", "exit-code"), []byte("2\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "spike-load.log"), []byte("stack start failed\n"), 0o644))

	code, out := runPromisecheckOutput(t, "-summarize", dir)
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "| drain | 0 | 120 | 0 | passed | held |")
	require.Contains(t, out, "| vote-fanout | 1 | 900 | 3 | invariants.no-nonce-left-pending | violated: ml_node_locks_released |")
	require.Contains(t, out, "| spike-load | 2 | 0 | 0 | no assertions.json | no report |")
	require.Contains(t, out, "# System promise checks over the runs")
	require.Contains(t, out, " **1** |")

	empty := t.TempDir()
	require.Equal(t, 2, runPromisecheck(t, "-summarize", empty), "a directory with no runs is not an empty baseline")
}

// Test flow:
//  1. Two baselines: drain held before and leaked after, vote-fanout unchanged, chunk-shapes only
//     before and spike-load only after.
//  2. Compare them.
//  3. Check changed cells are bold as before → after, unchanged ones plain, client counts shown for
//     both without a mark, a scenario on one side only shown as absent on the other, and the count
//     of changed cells.
func TestPromisecheckComparesTwoBaselines(t *testing.T) {
	before, after := t.TempDir(), t.TempDir()
	writeBaselineRun(t, before, "drain", "0", 120, 0, nil, heldReport())
	writeBaselineRun(t, after, "drain", "1", 118, 2, []string{"requests.error_rate"}, leakyReport())
	writeBaselineRun(t, before, "vote-fanout", "1", 900, 3, nil, leakyReport())
	writeBaselineRun(t, after, "vote-fanout", "1", 905, 3, nil, leakyReport())
	writeBaselineRun(t, before, "chunk-shapes", "0", 50, 0, nil, heldReport())
	writeBaselineRun(t, after, "spike-load", "0", 70, 0, nil, nil)

	code, out := runPromisecheckOutput(t, "-compare", before, after)
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "| drain | **0 → 1** | 120 / 118 | 0 / 2 | **passed → requests.error_rate** | **held → violated: ml_node_locks_released** |")
	require.Contains(t, out, " **ok → 1** |")
	require.Contains(t, out, "| vote-fanout | 1 | 900 / 905 | 3 / 3 | passed | violated: ml_node_locks_released |")
	require.Contains(t, out, "| chunk-shapes | **0 → absent** | 50 / absent |")
	require.Contains(t, out, "| spike-load | **absent → 0** | absent / 70 | absent / 0 | **absent → passed** | **absent → no report** |")
	require.Regexp(t, `\n\d+ changed cells\.\n`, out)

	require.Equal(t, 2, runPromisecheck(t, "-compare", before), "two baselines are needed")
	require.Equal(t, 2, runPromisecheck(t, "-compare", "-summarize", before, after))
}
