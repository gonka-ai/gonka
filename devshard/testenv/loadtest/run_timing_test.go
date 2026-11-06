package loadtest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Neither preparation nor workload introduces a deadline; user cancellation
// still propagates to both phases.
func TestPreparationAndRunHaveNoDeadline(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	timing := newRunTiming(parent, testScenario())
	defer timing.close()
	prep := timing.prepare()
	_, hasDeadline := prep.Deadline()
	require.False(t, hasDeadline)
	run := timing.startRun()
	_, hasDeadline = run.Deadline()
	require.False(t, hasDeadline)
	require.ErrorIs(t, prep.Err(), context.Canceled)
	require.NoError(t, run.Err())
	cancel()
	require.ErrorIs(t, run.Err(), context.Canceled)
}

// User cancellation must permit final HTTP reads,
// while the diagnostics budget must still put an upper bound on that work.
func TestDiagnosticsSurviveCancelledRunAndAreBounded(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	timing := newRunTiming(parent, testScenario())
	require.Equal(t, max(5*time.Minute, testScenario().DrainDuration()+2*time.Minute), timing.diagnostics)
	timing.diagnostics = 100 * time.Millisecond
	defer timing.close()
	timing.prepare()
	run := timing.startRun()
	cancel()
	require.ErrorIs(t, run.Err(), context.Canceled)
	diag := timing.startDiagnostics()
	require.NoError(t, diag.Err())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer server.Close()
	request, err := http.NewRequestWithContext(diag, http.MethodGet, server.URL, nil)
	require.NoError(t, err)
	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	response.Body.Close()
	<-diag.Done()
	require.ErrorIs(t, diag.Err(), context.DeadlineExceeded)
	require.Contains(t, context.Cause(diag).Error(), "diagnostics timeout")
}

// Exercise real client cancellation: retain completed requests and an in-flight
// cancelled request on disk, then verify rechecking cannot turn a partial run
// into PASS merely because its client error threshold would allow it.
func TestCancelledWorkloadKeepsPartialResultsAndFailsRecheck(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]string{{"text": "ok"}}})
			return
		}
		cancel()
		<-ctx.Done()
	}))
	defer server.Close()
	scenario := testScenario()
	scenario.Workload.Duration = "1s"
	scenario.Workload.MaxInFlight = 1
	scenario.Workload.Traffic = TrafficProfile{Type: "closed_loop"}
	dir := t.TempDir()
	summary, err := RunGenerator(ctx, GeneratorConfig{GatewayURL: server.URL, Scenario: scenario, OutputDir: dir})
	require.NoError(t, err)
	require.GreaterOrEqual(t, summary.Completed, 1)
	require.GreaterOrEqual(t, summary.Requests, 2)
	require.FileExists(t, filepath.Join(dir, "requests.jsonl"))
	var saved Summary
	body, err := os.ReadFile(filepath.Join(dir, "summary.json"))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(body, &saved))
	require.Equal(t, summary.Completed, saved.Completed)
	checks, err := evaluateRun(dir, scenario, summary, nil, RunInputs{RunError: "workload interrupted: context deadline exceeded"}, false)
	require.NoError(t, err)
	var completion *AssertionResult
	for i := range checks {
		if checks[i].Name == "run.completed" {
			completion = &checks[i]
		}
	}
	require.NotNil(t, completion)
	require.False(t, completion.Passed)
	require.Contains(t, completion.Actual, "context deadline exceeded")
}

func TestPreparationCancellationPermitsDiagnostics(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	timing := newRunTiming(parent, testScenario())
	defer timing.close()
	prep := timing.prepare()
	cancel()
	require.ErrorIs(t, context.Cause(prep), context.Canceled)
	require.NoError(t, timing.startDiagnostics().Err())
	deadline, bounded := timing.startDiagnostics().Deadline()
	require.True(t, bounded)
	require.WithinDuration(t, time.Now().Add(timing.diagnostics), deadline, time.Second)
}
