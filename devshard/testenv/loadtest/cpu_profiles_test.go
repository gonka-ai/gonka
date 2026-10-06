package loadtest

import (
	"context"
	"devshard/testenv/config"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestHostCPUProfileUsesPrivateAdminListener(t *testing.T) {
	dir := t.TempDir()
	proc := filepath.Join(dir, "proc")
	child := filepath.Join(proc, "123")
	require.NoError(t, os.MkdirAll(child, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(child, "comm"), []byte("devshardd\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(child, "environ"), []byte("SECRET=must-not-appear\x00DEVSHARD_ADMIN_ADDR=127.0.0.1:6001\x00"), 0o600))
	// Substitute wget to inspect the actual destination without Docker or HTTP.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "wget"), []byte("#!/bin/sh\nprintf '%s\\n' \"$@\"\n"), 0o755))
	run := func() ([]byte, error) {
		cmd := exec.Command("sh", "-c", hostCPUProfileScript, "profile", "30", proc)
		cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"))
		return cmd.CombinedOutput()
	}
	body, err := run()
	require.NoError(t, err, string(body))
	require.Contains(t, string(body), "http://127.0.0.1:6001/debug/pprof/profile?seconds=30")
	require.NotContains(t, string(body), "must-not-appear")
	require.Contains(t, string(body), "40")
	require.NoError(t, os.WriteFile(filepath.Join(child, "environ"), []byte("OTHER=value\x00"), 0o600))
	body, err = run()
	require.Error(t, err)
	require.Contains(t, string(body), "missing or unsupported")
	other := filepath.Join(proc, "124")
	require.NoError(t, os.MkdirAll(other, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(other, "comm"), []byte("devshardd\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(other, "environ"), nil, 0o600))
	body, err = run()
	require.Error(t, err)
	require.Contains(t, string(body), "exactly one")
}

func TestCPUProfilerSavesBinaryAndManifest(t *testing.T) {
	bytes := []byte{0x1f, 0x8b, 0, 1, 2, 3}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/debug/pprof/profile", r.URL.Path)
		require.Equal(t, "1", r.URL.Query().Get("seconds"))
		require.Equal(t, "Bearer test-key", r.Header.Get("Authorization"))
		_, _ = w.Write(bytes)
	}))
	defer server.Close()
	dir := t.TempDir()
	profiler, err := startCPUProfiler(context.Background(), RunnerConfig{OutputDir: dir}, &config.File{}, "", "", server.URL, "test-key", []CPUProfileWindow{{StartAfter: "0s", Duration: "1s"}})
	require.NoError(t, err)
	select {
	case <-profiler.done:
	case <-time.After(3 * time.Second):
		t.Fatal("profiler did not finish")
	}
	results := profiler.stop()
	require.Len(t, results, 1)
	require.Empty(t, results[0].Error)
	body, err := os.ReadFile(filepath.Join(dir, results[0].File))
	require.NoError(t, err)
	require.Equal(t, bytes, body)
	require.FileExists(t, filepath.Join(dir, "cpu-profiles.json"))
	require.Equal(t, results, profiler.stop())
}

func TestCPUProfilerCancellationDoesNotWaitForFutureWindow(t *testing.T) {
	profiler, err := startCPUProfiler(context.Background(), RunnerConfig{OutputDir: t.TempDir()}, &config.File{}, "", "", "", "", []CPUProfileWindow{{StartAfter: "1h", Duration: "1s"}})
	require.NoError(t, err)
	done := make(chan struct{})
	go func() { profiler.stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancellation did not stop scheduled profiling")
	}
}

func TestCPUProfileRejectsHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusUnauthorized) }))
	defer server.Close()
	body, err := fetchCPUProfile(context.Background(), server.URL, "")
	require.ErrorContains(t, err, "401")
	require.Nil(t, body)
}

func TestFreshScenarioAndProfileWindows(t *testing.T) {
	scenario, err := LoadScenario("scenarios/fresh-escrow-64rps-2m.yaml")
	require.NoError(t, err)
	require.Equal(t, 2*time.Minute, scenario.Duration())
	require.Equal(t, 10*time.Second, scenario.ReportInterval())
	require.Len(t, scenario.Diagnostics.CPUProfiles, 2)
	scenario.Diagnostics.CPUProfiles[1].StartAfter = "20s"
	require.ErrorContains(t, scenario.Validate(), "overlap")
	scenario.Diagnostics.CPUProfiles[1].StartAfter = "110s"
	require.ErrorContains(t, scenario.Validate(), "within workload")
}

func TestTenSecondLatencyWindows(t *testing.T) {
	scenario := testScenario()
	scenario.Workload.Duration = "25s"
	scenario.Workload.ReportInterval = "10s"
	start := time.Unix(1000, 0)
	windows := latencyWindows(scenario, start, []RequestResult{
		{StartedAt: start.Add(9 * time.Second), Duration: time.Millisecond, Outcome: "completed"},
		{StartedAt: start.Add(18 * time.Second), Duration: 2 * time.Millisecond, Outcome: "completed"},
		{StartedAt: start.Add(24 * time.Second), Outcome: "dropped_by_generator"},
	})
	require.Len(t, windows, 3)
	require.Equal(t, start.Add(10*time.Second), windows[1].StartedAt)
	require.Equal(t, 5*time.Second, windows[2].Duration)
	require.Equal(t, 1, windows[2].Dropped)
	scenario.Workload.ReportInterval = "0s"
	require.ErrorContains(t, scenario.Validate(), "report_interval")
}
