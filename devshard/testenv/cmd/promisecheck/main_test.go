package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
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
	cmd := exec.Command(os.Args[0], append([]string{"-test.run=^TestHelperProcess$", "--"}, args...)...)
	cmd.Env = append(os.Environ(), "PROMISECHECK_HELPER=1")
	out, err := cmd.CombinedOutput()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		t.Logf("promisecheck output:\n%s", out)
		return ee.ExitCode()
	}
	require.NoError(t, err, "%s", out)
	return 0
}

func writeRun(t *testing.T, requests string) string {
	t.Helper()
	dir := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(dir, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(body), 0o644))
	}
	write("run.yaml", "scenario: x\nworkload:\n  request:\n    model: test-model\n    messages:\n      - {role: user, content: \"p\"}\n")
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
