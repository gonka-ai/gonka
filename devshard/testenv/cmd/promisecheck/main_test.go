package main

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/testenv/citest/harness"
)

// TestHelperProcess runs main() when runPromisecheck re-executes the test binary.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("PROMISECHECK_HELPER") != "1" {
		t.Skip("helper process")
	}
	args := os.Args
	for i, a := range args {
		if a == "--" {
			args = args[i+1:]
			break
		}
	}
	os.Args = append([]string{"promisecheck"}, args...)
	main()
	os.Exit(0)
}

func runPromisecheck(t *testing.T, args ...string) int {
	t.Helper()
	code, _ := runPromisecheckOutput(t, args...)
	return code
}

// runPromisecheckOutput runs the CLI and returns its exit code and what it printed.
func runPromisecheckOutput(t *testing.T, args ...string) (int, string) {
	t.Helper()
	cmd := exec.Command(os.Args[0], append([]string{"-test.run=^TestHelperProcess$", "--"}, args...)...)
	cmd.Env = append(os.Environ(), "PROMISECHECK_HELPER=1")
	out, err := cmd.CombinedOutput()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		t.Logf("promisecheck output:\n%s", out)
		return ee.ExitCode(), string(out)
	}
	require.NoError(t, err, "%s", out)
	return 0, string(out)
}

func writeRun(t *testing.T, requests string) string {
	t.Helper()
	dir := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(dir, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(body), 0o644))
	}
	write("run.yaml", "scenario: x\nworkload:\n  request:\n    model: test-model\n    messages:\n      - {role: user, content: \"p\"}\ndiagnostics:\n  logs:\n    tail: all\n")
	write("requests.jsonl", requests)
	write("export/db-devshard-postgres-versiond-0/postgres/public.devshard_diffs.csv",
		"epoch_id,escrow_id,nonce,txs_proto,post_state_root\n")
	return dir
}

const completed = `{"request_id":"r-1","status":200,"outcome":"completed"}` + "\n"

// Test flow:
//  1. A run with no violation; the same run whose generator dropped a request at max_in_flight.
//  2. Run the CLI on each.
//  3. Check the first exits 0 and the second 3: the runner fails a run that is not valid, and the
//     CLI must not report it green.
func TestPromisecheckFailsARunThatIsNotValid(t *testing.T) {
	require.Equal(t, 0, runPromisecheck(t, "-loadtest", writeRun(t, completed)))
	dropped := writeRun(t, completed+`{"request_id":"r-2","outcome":"dropped_by_generator","error":"max_in_flight reached"}`+"\n")
	require.Equal(t, 3, runPromisecheck(t, "-loadtest", dropped))
}

// Test flow:
//  1. A run with no violation and a -report path whose directory does not exist.
//  2. Run the CLI.
//  3. Check it exits 2, as for a run it cannot read: exit 1 would report a violation the run did not
//     commit.
func TestPromisecheckReportWriteFailureIsNotAViolation(t *testing.T) {
	code := runPromisecheck(t, "-loadtest", writeRun(t, completed), "-report", filepath.Join(t.TempDir(), "missing", "r.md"))
	require.Equal(t, 2, code)
}

// Test flow:
//  1. A run whose mock-dapi journal holds a lock never released; a known list that names the leak.
//  2. Run the CLI without and with the known list, and once more with a grace that covers the lock.
//  3. Check the leak fails the run (exit 1), the known list labels it (exit 0), and a lock within
//     the grace is not judged (exit 0).
func TestPromisecheckJudgesTheLockJournal(t *testing.T) {
	dir := writeRun(t, completed)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "compose.log"), []byte(
		`mock-dapi-1  | {"time":"2026-10-19T07:58:00Z","msg":"mlnode_acquire","outcome":"acquired","lock_id":"a","node_id":"n4","request_id":"req-1"}`+"\n"+
			`mock-dapi-1  | {"time":"2026-10-19T08:05:00Z","msg":"mlnode_release","outcome":"SUCCESS","lock_id":"b","node_id":"n4"}`+"\n"), 0o644))
	require.Equal(t, 1, runPromisecheck(t, "-loadtest", dir))

	known := filepath.Join(t.TempDir(), "known.json")
	require.NoError(t, os.WriteFile(known, []byte(`[{"promise":"ml_node_locks_released","detail":"never released","ref":"tracked-defect"}]`), 0o644))
	require.Equal(t, 0, runPromisecheck(t, "-loadtest", dir, "-known", known))
	require.Equal(t, 0, runPromisecheck(t, "-loadtest", dir, "-lock-grace", "10m"),
		"a lock within the grace of the journal end is not judged")
}

// Test flow:
//  1. Two saved JSON reports, one with a violated check, in directories named after their runs.
//  2. Run the CLI with -render on one and -summarize on both.
//  3. Check -render prints the Markdown report titled by the run, and -summarize one row per run
//     with the violation count; neither needs -loadtest.
func TestPromisecheckRendersAndSummarizesSavedReports(t *testing.T) {
	save := func(name string, rep harness.PromiseReport) string {
		path := filepath.Join(t.TempDir(), name, "report.json")
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		raw, err := json.Marshal(rep)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(path, raw, 0o644))
		return path
	}
	violated := harness.CheckPromises(harness.PromiseInput{})
	violated.Checked[harness.NodeLocksReleased] = harness.Measure{Measured: true, Judged: 3}
	violated.Violations = []harness.PromiseViolation{{Promise: harness.NodeLocksReleased, Host: "n4", Detail: "1 of 3 locks on n4 never released"}}
	leaky := save("vote-fanout", violated)
	clean := save("catch-up", harness.CheckPromises(harness.PromiseInput{}))

	code, out := runPromisecheckOutput(t, "-render", leaky)
	require.Equal(t, 0, code)
	require.Contains(t, out, "System promise checks: vote-fanout")
	require.Contains(t, out, "1 of 3 locks on n4 never released")

	code, out = runPromisecheckOutput(t, "-summarize", leaky, "control="+clean)
	require.Equal(t, 0, code)
	require.Contains(t, out, "| vote-fanout |")
	require.Contains(t, out, "| control |", "name=path names the row")
	require.Contains(t, out, " **1** |")
	require.False(t, strings.Contains(out, "| catch-up |"))
}

// Test flow:
//  1. A run with no violation.
//  2. Run the CLI with a negative and a NaN burn allowance, and a negative lock grace.
//  3. Check each is refused as a bad command line (exit 2) rather than judged against nothing.
func TestPromisecheckRefusesANegativeOrNaNBound(t *testing.T) {
	dir := writeRun(t, completed)
	require.Equal(t, 2, runPromisecheck(t, "-loadtest", dir, "-burn-allowance", "-1"))
	require.Equal(t, 2, runPromisecheck(t, "-loadtest", dir, "-burn-allowance", "NaN"))
	require.Equal(t, 2, runPromisecheck(t, "-loadtest", dir, "-lock-grace", "-1m"))
}

// Test flow:
//  1. A run that breaks a property (a lock never released) and a saved report.
//  2. Run the CLI on the run together with -summarize or -render, and -summarize with nothing to read.
//  3. Check each is refused (exit 2) instead of skipping the run and exiting 0.
func TestPromisecheckRefusesRenderOrSummarizeWithARun(t *testing.T) {
	dir := writeRun(t, completed)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "compose.log"), []byte(
		`mock-dapi-1  | {"time":"2026-10-19T07:58:00Z","msg":"mlnode_acquire","outcome":"acquired","lock_id":"a","node_id":"n4","request_id":"req-1"}`+"\n"+
			`mock-dapi-1  | {"time":"2026-10-19T08:05:00Z","msg":"mlnode_release","outcome":"SUCCESS","lock_id":"b","node_id":"n4"}`+"\n"), 0o644))
	require.Equal(t, 1, runPromisecheck(t, "-loadtest", dir))
	saved := filepath.Join(t.TempDir(), "r", "report.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(saved), 0o755))
	raw, err := json.Marshal(harness.CheckPromises(harness.PromiseInput{}))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(saved, raw, 0o644))
	require.Equal(t, 2, runPromisecheck(t, "-loadtest", dir, "-summarize", saved), "the run is not silently skipped")
	require.Equal(t, 2, runPromisecheck(t, "-loadtest", dir, "-render", saved))
	require.Equal(t, 2, runPromisecheck(t, "-render", saved, "-summarize", saved))
	require.Equal(t, 2, runPromisecheck(t, "-summarize"))
}

// Test flow:
//  1. A saved report whose path holds '='.
//  2. Summarize it by its path alone and as name=path.
//  3. Check both read the report: an existing file is a path, not name=path.
func TestPromisecheckSummarizesAPathWithAnEqualsSign(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run=1", "report.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	raw, err := json.Marshal(harness.CheckPromises(harness.PromiseInput{}))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, raw, 0o644))
	code, out := runPromisecheckOutput(t, "-summarize", path)
	require.Equal(t, 0, code)
	require.Contains(t, out, "| run=1 |")
	code, out = runPromisecheckOutput(t, "-summarize", "named="+path)
	require.Equal(t, 0, code)
	require.Contains(t, out, "| named |")
}
