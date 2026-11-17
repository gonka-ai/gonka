package loadtest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"devshard/testenv/config"
	"github.com/stretchr/testify/require"
)

func TestSnapshotProfilerCapturesEndpoints(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer test-key", r.Header.Get("Authorization"))
		require.Empty(t, r.URL.Query().Get("gc"))
		switch r.URL.Path {
		case "/debug/pprof/heap":
			_, _ = w.Write([]byte{31, 139, 1, 2})
		case "/debug/pprof/goroutine":
			require.Equal(t, "2", r.URL.Query().Get("debug"))
			_, _ = w.Write([]byte("goroutine 1 [running]:\n"))
		default:
			t.Errorf("unexpected endpoint: %s", r.URL)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	dir := t.TempDir()
	p, err := startSnapshotProfiler(context.Background(), RunnerConfig{OutputDir: dir}, &config.File{}, "", "", server.URL, "test-key", Diagnostics{
		HeapProfiles: []ProfileSnapshot{{StartAfter: "0s"}}, GoroutineProfiles: []ProfileSnapshot{{StartAfter: "0s"}},
	})
	require.NoError(t, err)
	select {
	case <-p.done:
	case <-time.After(3 * time.Second):
		t.Fatal("snapshot timeout")
	}
	results := p.stop()
	require.Len(t, results, 2)
	for _, r := range results {
		require.Empty(t, r.Error)
		require.FileExists(t, filepath.Join(dir, r.File))
	}
	require.FileExists(t, filepath.Join(dir, "profile-snapshots.json"))
	require.Equal(t, results, p.stop())
}

func TestSnapshotProfilerCancellationAndTargets(t *testing.T) {
	opts := RunnerConfig{OutputDir: t.TempDir()}
	_, err := startSnapshotProfiler(context.Background(), opts, &config.File{}, "", "", "", "", Diagnostics{HeapProfiles: []ProfileSnapshot{{StartAfter: "0s", Targets: []string{"versiond-2"}}}})
	require.ErrorContains(t, err, "not configured")
	p, err := startSnapshotProfiler(context.Background(), opts, &config.File{}, "", "", "", "", Diagnostics{HeapProfiles: []ProfileSnapshot{{StartAfter: "1h"}}})
	require.NoError(t, err)
	done := make(chan struct{})
	go func() { p.stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancellation stalled")
	}
	require.Empty(t, p.results)
}

func TestSnapshotProfilerRecordsHTTPFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(403) }))
	defer server.Close()
	p, err := startSnapshotProfiler(context.Background(), RunnerConfig{OutputDir: t.TempDir()}, &config.File{}, "", "", server.URL, "", Diagnostics{HeapProfiles: []ProfileSnapshot{{StartAfter: "0s"}}})
	require.NoError(t, err)
	select {
	case <-p.done:
	case <-time.After(3 * time.Second):
		t.Fatal("snapshot timeout")
	}
	results := p.stop()
	require.Len(t, results, 1)
	require.Contains(t, results[0].Error, "403")
	require.Empty(t, results[0].File)
}

func TestHostSnapshotUsesPrivateAdminListener(t *testing.T) {
	dir := t.TempDir()
	proc := filepath.Join(dir, "proc")
	child := filepath.Join(proc, "123")
	require.NoError(t, os.MkdirAll(child, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(child, "comm"), []byte("devshardd\n"), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(child, "environ"), []byte("SECRET=hidden\x00DEVSHARD_ADMIN_ADDR=127.0.0.1:6001\x00"), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "wget"), []byte("#!/bin/sh\nprintf '%s\\n' \"$@\"\n"), 0755))
	for _, endpoint := range []string{"heap", "goroutine?debug=2"} {
		cmd := exec.Command("sh", "-c", hostSnapshotScript, "snapshot", "0", proc, endpoint)
		cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"))
		body, err := cmd.CombinedOutput()
		require.NoError(t, err, string(body))
		require.Contains(t, string(body), "http://127.0.0.1:6001/debug/pprof/"+endpoint)
		require.NotContains(t, string(body), "hidden")
	}
}

func TestSnapshotScenarioValidation(t *testing.T) {
	s, err := LoadScenario("scenarios/catch-up.yaml")
	require.NoError(t, err)
	require.Len(t, s.Diagnostics.HeapProfiles, 6)
	require.Len(t, s.Diagnostics.GoroutineProfiles, 2)
	for _, kind := range []string{"heap", "goroutine"} {
		for _, snapshots := range [][]ProfileSnapshot{
			{{StartAfter: "bad"}}, {{StartAfter: "-1s"}}, {{StartAfter: "5m"}},
			{{StartAfter: "1s"}, {StartAfter: "1s"}}, {{StartAfter: "2s"}, {StartAfter: "1s"}},
			{{StartAfter: "1s", Targets: []string{"versiond-4"}}}, {{StartAfter: "1s", Targets: []string{"gateway", "gateway"}}},
		} {
			c := s
			if kind == "heap" {
				c.Diagnostics.HeapProfiles = snapshots
			} else {
				c.Diagnostics.GoroutineProfiles = snapshots
			}
			require.Error(t, c.Validate())
		}
	}
}
