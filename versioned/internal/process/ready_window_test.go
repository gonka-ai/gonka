package process

import (
	"context"
	"net/http"
	"testing"
	"time"

	"versioned/internal/config"
)

func TestNextReadyWindowDoublesUpToMax(t *testing.T) {
	max := 32 * time.Minute
	got := time.Minute
	want := []time.Duration{
		2 * time.Minute,
		4 * time.Minute,
		8 * time.Minute,
		16 * time.Minute,
		32 * time.Minute,
		32 * time.Minute,
		32 * time.Minute,
	}
	for i, expect := range want {
		got = nextReadyWindow(got, max)
		if got != expect {
			t.Fatalf("step %d: nextReadyWindow = %v, want %v", i+1, got, expect)
		}
	}
}

func TestNextReadyWindowCapsWhenAlreadyAboveMax(t *testing.T) {
	if got := nextReadyWindow(60*time.Minute, 32*time.Minute); got != 32*time.Minute {
		t.Fatalf("nextReadyWindow = %v, want %v", got, 32*time.Minute)
	}
}

func TestNextReadyWindowHandlesOverflow(t *testing.T) {
	max := 32 * time.Minute
	if got := nextReadyWindow(time.Duration(1)<<62, max); got != max {
		t.Fatalf("nextReadyWindow on overflow = %v, want %v", got, max)
	}
}

func TestReadyWindowEventuallyReachesCeiling(t *testing.T) {
	max := 32 * time.Minute
	w := 60 * time.Second
	attempts := 0
	for w < max {
		w = nextReadyWindow(w, max)
		attempts++
		if attempts > 10 {
			t.Fatalf("window did not reach ceiling: stuck at %v", w)
		}
	}
	if w != max {
		t.Fatalf("final window = %v, want %v", w, max)
	}
	if attempts != 5 {
		t.Fatalf("attempts to reach ceiling = %d, want 5 (60s->2m->4m->8m->16m->32m)", attempts)
	}
}

func TestManagerDefaultsReadyMaxWait(t *testing.T) {
	m := NewManager(config.Config{BinDir: t.TempDir(), DataDir: t.TempDir()})
	if m.cfg.ReadyMaxWait != 32*time.Minute {
		t.Fatalf("default ReadyMaxWait = %v, want 32m", m.cfg.ReadyMaxWait)
	}
}

func TestManagerReadyMaxWaitNeverBelowReadyTimeout(t *testing.T) {
	m := NewManager(config.Config{
		BinDir:       t.TempDir(),
		DataDir:      t.TempDir(),
		ReadyTimeout: 10 * time.Minute,
		ReadyMaxWait: time.Minute,
	})
	if m.cfg.ReadyMaxWait != 10*time.Minute {
		t.Fatalf("ReadyMaxWait = %v, want it raised to ReadyTimeout (10m)", m.cfg.ReadyMaxWait)
	}
}

func TestProbeAllowsReadyWaitExtension(t *testing.T) {
	if !probeAllowsReadyWaitExtension(readyProbeInitializing) {
		t.Fatal("initializing should extend the window")
	}
	if !probeAllowsReadyWaitExtension(readyProbeReadyAbsent) {
		t.Fatal("missing /ready should extend the window for older binaries")
	}
	for _, r := range []readyProbeResult{readyProbeUnreachable, readyProbeNotReady, readyProbeReady} {
		if probeAllowsReadyWaitExtension(r) {
			t.Fatalf("%s should not extend the window", r)
		}
	}
}

func TestReadyPathAbsent(t *testing.T) {
	if !readyPathAbsent(http.StatusNotFound) {
		t.Fatal("Echo returns 404 for an unregistered /ready; that must count as absent")
	}
	if !readyPathAbsent(http.StatusMethodNotAllowed) || !readyPathAbsent(http.StatusNotImplemented) {
		t.Fatal("405/501 must count as absent, matching the legacy /ready fallback")
	}
	if readyPathAbsent(http.StatusServiceUnavailable) || readyPathAbsent(http.StatusOK) {
		t.Fatal("503/200 are not an absent /ready route")
	}
}

func TestReadyBodyInitializing(t *testing.T) {
	tests := []struct {
		name string
		body string
		want bool
	}{
		{name: "chain not ready", body: `{"ready":false,"draining":false,"storage_ready":true}`, want: true},
		{name: "storage rebuilding", body: `{"ready":true,"draining":false,"storage_ready":false}`, want: true},
		{name: "both not ready", body: `{"ready":false,"draining":false,"storage_ready":false}`, want: true},
		{name: "draining", body: `{"ready":false,"draining":true,"storage_ready":true}`, want: false},
		{name: "fully ready body", body: `{"ready":true,"draining":false,"storage_ready":true}`, want: false},
		{name: "unparseable", body: `not json`, want: false},
		{name: "empty", body: ``, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := readyBodyInitializing([]byte(tt.body)); got != tt.want {
				t.Fatalf("readyBodyInitializing(%s) = %v, want %v", tt.body, got, tt.want)
			}
		})
	}
}

func TestWaitForChildServingReadyExtendsWhileInitializing(t *testing.T) {
	readyAt := time.Now().Add(350 * time.Millisecond)
	adminPort, adminShutdown := startLocalHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ready" {
			http.NotFound(w, r)
			return
		}
		if time.Now().Before(readyAt) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"ready":false,"draining":false,"storage_ready":false}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ready":true,"draining":false,"storage_ready":true}`))
	}))
	defer adminShutdown()

	publicPort, publicShutdown := startLocalHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer publicShutdown()

	c := &child{port: publicPort}
	setTestAdminPort(c, adminPort)
	start := time.Now()
	ready, last := waitForChildServingReadyUntil(context.Background(), c, "/ready", 80*time.Millisecond, time.Second)
	if !ready {
		t.Fatalf("initializing child should be waited for past minWait, last probe = %s", last)
	}
	if elapsed := time.Since(start); elapsed < 300*time.Millisecond {
		t.Fatalf("became ready too fast (%s); expected to wait through initializing", elapsed)
	}
}

func TestWaitForChildServingReadyFailsPromptlyWhenUnreachable(t *testing.T) {
	port, shutdown := startLocalHTTPServer(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	shutdown()

	c := &child{port: port}
	start := time.Now()
	ready, last := waitForChildServingReadyUntil(context.Background(), c, "/ready", 150*time.Millisecond, 2*time.Second)
	if ready {
		t.Fatal("unreachable child should not become ready")
	}
	if last != readyProbeUnreachable {
		t.Fatalf("last probe = %s, want unreachable", last)
	}
	if elapsed := time.Since(start); elapsed > 600*time.Millisecond {
		t.Fatalf("elapsed %s, want fail at minWait rather than maxWait", elapsed)
	}
}

func TestWaitForChildServingReadyDoesNotExtendWhenDraining(t *testing.T) {
	adminPort, adminShutdown := startLocalHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"ready":false,"draining":true,"storage_ready":true}`))
	}))
	defer adminShutdown()

	publicPort, publicShutdown := startLocalHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer publicShutdown()

	c := &child{port: publicPort}
	setTestAdminPort(c, adminPort)
	start := time.Now()
	ready, last := waitForChildServingReadyUntil(context.Background(), c, "/ready", 80*time.Millisecond, time.Second)
	if ready {
		t.Fatal("draining child should not be treated as initializing")
	}
	if last != readyProbeNotReady {
		t.Fatalf("last probe = %s, want not_ready", last)
	}
	if elapsed := time.Since(start); elapsed > 400*time.Millisecond {
		t.Fatalf("elapsed %s, want fail at minWait rather than maxWait", elapsed)
	}
}

func TestWaitForChildServingReadyExtendsWhenReadyPathAbsent(t *testing.T) {
	readyAt := time.Now().Add(350 * time.Millisecond)
	adminPort, adminShutdown := startLocalHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ready" || time.Now().Before(readyAt) {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ready":true,"draining":false,"storage_ready":true}`))
	}))
	defer adminShutdown()

	publicPort, publicShutdown := startLocalHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer publicShutdown()

	c := &child{port: publicPort}
	setTestAdminPort(c, adminPort)
	start := time.Now()
	ready, last := waitForChildServingReadyUntil(context.Background(), c, "/ready", 80*time.Millisecond, time.Second)
	if !ready {
		t.Fatalf("404 on /ready should keep waiting like the legacy growing window, last probe = %s", last)
	}
	if elapsed := time.Since(start); elapsed < 300*time.Millisecond {
		t.Fatalf("became ready too fast (%s); expected to wait through missing /ready", elapsed)
	}
}
