package process

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"versioned/internal/config"
	"versioned/internal/oracle"
)

// readyBodyServer serves /ready with a configurable status code and
// recovery_complete field. The field state is an atomic int32 so the test
// goroutine and the handler goroutine never race on a plain *bool:
//
//	0 = field absent (pre-v5 body), 1 = recovery_complete:false, 2 = true.
type readyBodyServer struct {
	status atomic.Int32 // HTTP status code; 0 means 200
	field  atomic.Int32 // recovery_complete state (0/1/2)
}

const (
	fieldAbsent int32 = 0
	fieldFalse  int32 = 1
	fieldTrue   int32 = 2
)

func (s *readyBodyServer) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ready" {
			http.NotFound(w, r)
			return
		}
		code := int(s.status.Load())
		if code == 0 {
			code = http.StatusOK
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		switch s.field.Load() {
		case fieldAbsent:
			_, _ = w.Write([]byte(`{"storage_ready":true}`))
		case fieldTrue:
			_, _ = w.Write([]byte(`{"recovery_complete":true,"sessions_pending":0}`))
		default: // fieldFalse
			_, _ = w.Write([]byte(`{"recovery_complete":false,"sessions_pending":4}`))
		}
	})
}

func newRecoveryWaitManager(t *testing.T, recoveryTimeout time.Duration) *Manager {
	t.Helper()
	return NewManager(config.Config{
		BinaryName:      "devshardd",
		BasePort:        5000,
		ReadyPath:       "/ready",
		RecoveryTimeout: recoveryTimeout,
	})
}

// Test 1: overlap waits then cuts over once recovery_complete flips true.
func TestWaitForChildRecoveryComplete_WaitsThenCutsOverWhenWarm(t *testing.T) {
	srv := &readyBodyServer{}
	srv.field.Store(fieldFalse)
	adminPort, shutdown := startLocalHTTPServer(t, srv.handler())
	defer shutdown()

	m := newRecoveryWaitManager(t, 10*time.Second)
	newChild := &child{version: oracle.Version{Name: "v4"}}
	newChild.adminPort.Store(int64(adminPort))
	old := &child{version: oracle.Version{Name: "v4"}, status: statusRunning, done: make(chan struct{})}

	done := make(chan error, 1)
	go func() {
		done <- m.waitForChildRecoveryComplete(context.Background(), newChild, old, m.cfg.RecoveryTimeout)
	}()

	// Must still be waiting after a couple of tick intervals.
	select {
	case err := <-done:
		t.Fatalf("wait returned before recovery completed: %v", err)
	case <-time.After(3 * childReadyInterval):
	}

	srv.field.Store(fieldTrue)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("expected nil after recovery complete, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("wait did not return after recovery_complete became true")
	}
}

// Test 2: field absent → skip the wait (cold cutover, flow B).
func TestWaitForChildRecoveryComplete_AbsentFieldSkipsWait(t *testing.T) {
	srv := &readyBodyServer{}
	srv.field.Store(fieldAbsent)
	adminPort, shutdown := startLocalHTTPServer(t, srv.handler())
	defer shutdown()

	m := newRecoveryWaitManager(t, 2*time.Second)
	newChild := &child{version: oracle.Version{Name: "v4"}}
	newChild.adminPort.Store(int64(adminPort))
	old := &child{version: oracle.Version{Name: "v4"}, status: statusRunning, done: make(chan struct{})}

	start := time.Now()
	err := m.waitForChildRecoveryComplete(context.Background(), newChild, old, m.cfg.RecoveryTimeout)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("absent field should skip the wait (nil), got %v", err)
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("absent field waited %s; should return immediately", elapsed)
	}
}

// Test 3: old child stops being Running → publish immediately.
func TestWaitForChildRecoveryComplete_OldChildDeathPublishesImmediately(t *testing.T) {
	srv := &readyBodyServer{}
	srv.field.Store(fieldFalse) // never completes
	adminPort, shutdown := startLocalHTTPServer(t, srv.handler())
	defer shutdown()

	m := newRecoveryWaitManager(t, 30*time.Second)
	newChild := &child{version: oracle.Version{Name: "v4"}}
	newChild.adminPort.Store(int64(adminPort))
	oldDone := make(chan struct{})
	old := &child{version: oracle.Version{Name: "v4"}, status: statusRunning, done: oldDone}

	done := make(chan error, 1)
	go func() {
		done <- m.waitForChildRecoveryComplete(context.Background(), newChild, old, m.cfg.RecoveryTimeout)
	}()

	// Let it poll once, then kill the old child.
	time.Sleep(2 * childReadyInterval)
	m.mu.Lock()
	old.status = statusStopped
	m.mu.Unlock()
	close(oldDone)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("old-child death should publish (nil), got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("wait did not return after old child stopped")
	}
}

// Test 4: timeout aborts with ErrRecoveryTimeout; old keeps serving.
func TestWaitForChildRecoveryComplete_TimeoutAborts(t *testing.T) {
	srv := &readyBodyServer{}
	srv.field.Store(fieldFalse) // never completes
	adminPort, shutdown := startLocalHTTPServer(t, srv.handler())
	defer shutdown()

	m := newRecoveryWaitManager(t, 80*time.Millisecond)
	newChild := &child{version: oracle.Version{Name: "v4"}}
	newChild.adminPort.Store(int64(adminPort))
	old := &child{version: oracle.Version{Name: "v4"}, status: statusRunning, done: make(chan struct{})}

	err := m.waitForChildRecoveryComplete(context.Background(), newChild, old, m.cfg.RecoveryTimeout)
	if err == nil {
		t.Fatal("expected ErrRecoveryTimeout, got nil")
	}
	if err.Error() != ErrRecoveryTimeout.Error() {
		t.Fatalf("expected ErrRecoveryTimeout, got %v", err)
	}
}

// Test 5: hostDraining aborts with ErrHostDraining.
func TestWaitForChildRecoveryComplete_HostDrainingAborts(t *testing.T) {
	srv := &readyBodyServer{}
	srv.field.Store(fieldFalse)
	adminPort, shutdown := startLocalHTTPServer(t, srv.handler())
	defer shutdown()

	m := newRecoveryWaitManager(t, 30*time.Second)
	m.hostDraining = true
	newChild := &child{version: oracle.Version{Name: "v4"}}
	newChild.adminPort.Store(int64(adminPort))
	old := &child{version: oracle.Version{Name: "v4"}, status: statusRunning, done: make(chan struct{})}

	err := m.waitForChildRecoveryComplete(context.Background(), newChild, old, m.cfg.RecoveryTimeout)
	if !errors.Is(err, ErrHostDraining) {
		t.Fatalf("expected ErrHostDraining, got %v", err)
	}
}

// Test 6: ctx done aborts.
func TestWaitForChildRecoveryComplete_ContextCancelAborts(t *testing.T) {
	srv := &readyBodyServer{}
	srv.field.Store(fieldFalse)
	adminPort, shutdown := startLocalHTTPServer(t, srv.handler())
	defer shutdown()

	m := newRecoveryWaitManager(t, 30*time.Second)
	newChild := &child{version: oracle.Version{Name: "v4"}}
	newChild.adminPort.Store(int64(adminPort))
	old := &child{version: oracle.Version{Name: "v4"}, status: statusRunning, done: make(chan struct{})}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- m.waitForChildRecoveryComplete(ctx, newChild, old, m.cfg.RecoveryTimeout) }()

	time.Sleep(2 * childReadyInterval)
	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected error after ctx cancel, got nil")
		}
	case <-time.After(time.Second):
		t.Fatal("wait did not return after ctx cancel")
	}
}

// Test 7: legacy child (no admin port) skips the wait.
func TestWaitForChildRecoveryComplete_LegacyChildSkipsWait(t *testing.T) {
	m := newRecoveryWaitManager(t, 30*time.Second)
	newChild := &child{version: oracle.Version{Name: "v4"}} // adminPort stays 0
	old := &child{version: oracle.Version{Name: "v4"}, status: statusRunning, done: make(chan struct{})}

	err := m.waitForChildRecoveryComplete(context.Background(), newChild, old, m.cfg.RecoveryTimeout)
	if err != nil {
		t.Fatalf("legacy child should skip the wait (nil), got %v", err)
	}
}

// Test 7b: an unset RecoveryTimeout must normalize to 30m, not stay zero.
//
// This guards a quiet, high-blast-radius failure. waitForChildRecoveryComplete
// builds its poll window with context.WithTimeout(ctx, timeout); a zero timeout
// is already expired, so the first select would return ErrRecoveryTimeout and
// downloadAndSwap would abort *every* overlap swap while the old child kept
// serving. Rolling updates would silently stop applying with only a warn log.
// Nothing else pins the normalizeConfig default, so assert both the config
// value and the end-to-end effect (a warm child is still published).
func TestRecoveryTimeoutDefaultsWhenUnset(t *testing.T) {
	m := NewManager(config.Config{BinaryName: "devshardd", BasePort: 5000, ReadyPath: "/ready"})
	if m.cfg.RecoveryTimeout != 30*time.Minute {
		t.Fatalf("unset RecoveryTimeout = %v, want 30m (zero would abort every overlap swap)",
			m.cfg.RecoveryTimeout)
	}

	srv := &readyBodyServer{}
	srv.field.Store(fieldTrue)
	adminPort, shutdown := startLocalHTTPServer(t, srv.handler())
	defer shutdown()

	newChild := &child{version: oracle.Version{Name: "v4"}}
	newChild.adminPort.Store(int64(adminPort))
	old := &child{version: oracle.Version{Name: "v4"}, status: statusRunning, done: make(chan struct{})}
	if err := m.waitForChildRecoveryComplete(context.Background(), newChild, old, m.cfg.RecoveryTimeout); err != nil {
		t.Fatalf("warm child must publish under the defaulted timeout, got %v", err)
	}
}

// Test 7c: the stop/start (non-overlap) branch never reaches the warm wait.
//
// Plan Step 9 gates the wait to the overlap branch: with no healthy old
// generation to keep serving, waiting is just an outage. Today that holds
// structurally — downloadAndSwap returns inside the !rollingOverlapAllowed
// branch before the wait — so pin the predicate that carries it. A devshardd
// child whose storage is not postgres-only must not be overlap-eligible, which
// is what routes it to stop/start and past the warm wait.
func TestRollingOverlapDisallowedKeepsWarmWaitOutOfStopStart(t *testing.T) {
	m := newRecoveryWaitManager(t, 30*time.Minute)
	if m.rollingOverlapAllowed("v4", &child{storageMode: "hybrid"}, "postgres") {
		t.Fatal("hybrid old child must not be overlap-eligible; stop/start must skip the warm wait")
	}
	if m.rollingOverlapAllowed("v4", &child{storageMode: "postgres"}, "hybrid") {
		t.Fatal("hybrid new binary must not be overlap-eligible; stop/start must skip the warm wait")
	}
	if !m.rollingOverlapAllowed("v4", &child{storageMode: "postgres"}, "postgres") {
		t.Fatal("postgres-only pair must stay overlap-eligible so the warm wait still runs")
	}
}

// Test 8 (versiond item 6): the readiness monitor never consults the body.
// A 503 with a body that says recovery_complete:true must still flip serving
// to false — the monitor keys on status code alone. If recovery ever gated
// the monitor, the host would leave the HAProxy pool for the whole backlog.
func TestWatchChildReadiness_NeverReadsBody_RecoveryCompleteTrueDoesNotOverrideStatus(t *testing.T) {
	adminPort, shutdown := startLocalHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ready" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"recovery_complete":true}`))
	}))
	defer shutdown()

	m := NewManager(config.Config{BasePort: 5000, ReadyPath: "/ready"})
	c := &child{
		version: oracle.Version{Name: "v4"},
		port:    1,
		done:    make(chan struct{}),
		status:  statusRunning,
	}
	c.serving.Store(true)
	c.servingAt.Store(time.Now().UnixNano())
	setTestAdminPort(c, adminPort)

	ctx, cancel := context.WithCancel(context.Background())
	monitorDone := make(chan struct{})
	go func() {
		defer close(monitorDone)
		m.watchChildReadiness(ctx, c)
	}()

	if !waitFor(3*time.Second, func() bool { return !c.serving.Load() }) {
		cancel()
		<-monitorDone
		t.Fatal("monitor never observed the 503; if it read the body it would have stayed true")
	}
	cancel()
	<-monitorDone
}
