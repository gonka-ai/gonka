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

// A slow preparation must not consume the run deadline. Expiring preparation
// must leave the parent usable, and the recorded cause must identify the phase.
func TestRunBudgetStartsAfterPreparation(t *testing.T) {
	timing, err := newRunTiming(context.Background(), RunnerConfig{PreparationTimeout: time.Second, RunTimeout: 100 * time.Millisecond}, testScenario())
	require.NoError(t, err)
	defer timing.close()
	prep := timing.prepare()
	timer := time.NewTimer(120 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-prep.Done():
		t.Fatal("preparation ended early")
	case <-timer.C:
	}
	run := timing.startRun()
	deadline, _ := run.Deadline()
	require.Greater(t, time.Until(deadline), 80*time.Millisecond)
	<-run.Done()
	require.ErrorIs(t, context.Cause(run), context.DeadlineExceeded)
	require.Contains(t, context.Cause(run).Error(), "run timeout")
}

// Both a run deadline and a user cancellation must permit final HTTP reads,
// while the diagnostics budget must still put an upper bound on that work.
func TestDiagnosticsSurviveCancelledRunAndAreBounded(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	timing, err := newRunTiming(parent, RunnerConfig{DiagnosticsTimeout: 100 * time.Millisecond}, testScenario())
	require.NoError(t, err)
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

func TestPreparationTimeoutIsReportedWithoutCancellingParent(t *testing.T) {
	timing, err := newRunTiming(context.Background(), RunnerConfig{PreparationTimeout: 10 * time.Millisecond}, testScenario())
	require.NoError(t, err)
	defer timing.close()
	prep := timing.prepare()
	<-prep.Done()
	require.ErrorIs(t, context.Cause(prep), context.DeadlineExceeded)
	require.Contains(t, context.Cause(prep).Error(), "preparation timeout")
	require.NoError(t, timing.parent.Err())
	require.NoError(t, timing.startDiagnostics().Err())
}
