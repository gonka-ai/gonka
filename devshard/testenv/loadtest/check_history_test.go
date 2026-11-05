package loadtest

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Test flow:
// 1. Record four runs of one known_bug check in a history file: XPASS, XFAIL, XPASS, XPASS.
// 2. Require the last run to count 2 consecutive XPASS runs and its details to ask for 5 before dropping the marker.
// 3. Record the last run again and require the count to stay 2.
// 4. Record three more XPASS runs and require 5, with details that say to drop the marker.
// 5. Record a run older than all of them and require its count to cover only runs up to it.
func TestCheckHistoryAsksForFiveConsecutiveXPass(t *testing.T) {
	path := filepath.Join(t.TempDir(), checkHistoryFile)
	key := historyKey{Scenario: "prod-like-15m", Gateway: "devshardctl"}
	check := func(status string) []AssertionResult {
		return []AssertionResult{
			{Name: "invariants.error-rate-and-latency.timeout-vote-fanout.error_rate", Case: "timeout-vote-fanout", Status: status, Passed: true, Details: "known_bug #1988"},
			{Name: "requests.error_rate", Passed: true},
		}
	}
	record := func(offset time.Duration, status string) []AssertionResult {
		t.Helper()
		run := filepath.Join("runs", offset.String())
		marked, err := recordCheckHistory(path, key, run, at(offset), check(status))
		require.NoError(t, err)
		return marked
	}

	record(time.Hour, "XPASS")
	record(2*time.Hour, "XFAIL")
	record(3*time.Hour, "XPASS")
	marked := record(4*time.Hour, "XPASS")
	require.Equal(t, "known_bug #1988; XPASS 2 runs in a row of the 5 needed to drop the marker", marked[0].Details)
	require.Equal(t, check("XPASS")[1], marked[1])

	marked = record(4*time.Hour, "XPASS")
	require.Contains(t, marked[0].Details, "XPASS 2 runs in a row")

	record(5*time.Hour, "XPASS")
	record(6*time.Hour, "XPASS")
	marked = record(7*time.Hour, "XPASS")
	require.Equal(t, "known_bug #1988; XPASS 5 runs in a row: drop the marker", marked[0].Details)

	marked = record(0, "XPASS")
	require.Contains(t, marked[0].Details, "XPASS 1 runs in a row")
}

// Test flow:
// 1. Record a run whose max_pending_age holds and one with a single record over the bound; require the second to pass, its details saying FIX when it repeats.
// 2. Record the next run with a single record over the bound again; require it to fail and name the earlier run.
// 3. Record four runs that hold and then one with a single record over; require it to pass, since no run over the bound is among the 5 last runs.
// 4. Judge the second run again; require it to still pass, the later runs not counting.
// 5. Record a run with two records over the bound; require it to fail with no earlier run over the bound needed.
func TestCheckHistoryFixesMaxPendingAgeOnlyWhenItRepeats(t *testing.T) {
	path := filepath.Join(t.TempDir(), checkHistoryFile)
	key := historyKey{Scenario: "prod-like-15m", Gateway: "gateway"}
	check := func(records int) []AssertionResult {
		age := AssertionResult{Name: "invariants.no-nonce-left-pending.max_pending_age", Passed: records == 0, Actual: fmt.Sprintf("records_over=%d", records)}
		if records == 1 {
			age.Details, age.fixOnRepeat = "escrow 13 inference 644 (pending) on versiond-5", true
		}
		return []AssertionResult{age, {Name: "requests.error_rate", Passed: true}}
	}
	record := func(offset time.Duration, records int) AssertionResult {
		t.Helper()
		marked, err := recordCheckHistory(path, key, filepath.Join("runs", offset.String()), at(offset), check(records))
		require.NoError(t, err)
		require.Equal(t, check(0)[1], marked[1])
		return marked[0]
	}

	require.True(t, record(time.Hour, 0).Passed)
	once := record(2*time.Hour, 1)
	require.True(t, once.Passed, once.Details)
	require.Equal(t, "escrow 13 inference 644 (pending) on versiond-5; FIX only if it repeats: no record over the bound in the 4 runs before", once.Details)

	repeat := record(3*time.Hour, 1)
	require.False(t, repeat.Passed)
	require.Equal(t, "escrow 13 inference 644 (pending) on versiond-5; repeats "+filepath.Join("runs", (2*time.Hour).String())+" within the last 5 runs", repeat.Details)

	for offset := 4 * time.Hour; offset <= 7*time.Hour; offset += time.Hour {
		require.True(t, record(offset, 0).Passed)
	}
	require.True(t, record(8*time.Hour, 1).Passed)

	require.True(t, record(2*time.Hour, 1).Passed)

	require.False(t, record(9*time.Hour, 2).Passed)
}
