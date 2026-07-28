package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"versioned/internal/config"
	"versioned/internal/host"
	"versioned/internal/process"
)

type fakeHostShutdownManager struct {
	mu               sync.Mutex
	calls            []string
	waitChildrenIdle func(context.Context) error
	shutdown         func(context.Context) error
	forceCalled      chan struct{}
	forceOnce        sync.Once
}

func newFakeHostShutdownManager() *fakeHostShutdownManager {
	return &fakeHostShutdownManager{forceCalled: make(chan struct{})}
}

func (m *fakeHostShutdownManager) RequestChildrenDrain(context.Context) error {
	m.record("request_children_drain")
	return nil
}

func (m *fakeHostShutdownManager) WaitChildrenIdle(ctx context.Context) error {
	m.record("wait_children_idle")
	if m.waitChildrenIdle != nil {
		return m.waitChildrenIdle(ctx)
	}
	return nil
}

func (m *fakeHostShutdownManager) Shutdown(ctx context.Context) error {
	m.record("shutdown")
	if m.shutdown != nil {
		return m.shutdown(ctx)
	}
	return nil
}

func (m *fakeHostShutdownManager) ForceStopChildren() {
	m.record("force_stop_children")
	m.forceOnce.Do(func() {
		close(m.forceCalled)
	})
}

func (m *fakeHostShutdownManager) record(call string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, call)
}

func (m *fakeHostShutdownManager) callLog() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return strings.Join(m.calls, "\n")
}

func TestShutdownHostCompletesLifecycle(t *testing.T) {
	hostLifecycle := host.NewController()
	if err := hostLifecycle.Transition(host.StateServing); err != nil {
		t.Fatal(err)
	}
	if err := hostLifecycle.Transition(host.StateDraining); err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	mgr := process.NewManager(config.Config{
		BasePort:       5000,
		DrainKillGrace: time.Second,
	})
	force := make(chan struct{})
	pollDone := make(chan struct{})
	close(pollDone)

	if err := shutdownHost(
		config.Config{HostShutdownBudget: time.Second},
		server.Config,
		mgr,
		hostLifecycle,
		force,
		pollDone,
	); err != nil {
		t.Fatal(err)
	}
	if got := hostLifecycle.Snapshot().State; got != host.StateStopped {
		t.Fatalf("host state = %s, want stopped", got)
	}
}

func TestShutdownHostHonorsForceSignal(t *testing.T) {
	hostLifecycle := host.NewController()
	if err := hostLifecycle.Transition(host.StateDraining); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	mgr := process.NewManager(config.Config{BasePort: 5000})
	force := make(chan struct{})
	close(force)
	pollDone := make(chan struct{})
	close(pollDone)

	if err := shutdownHost(
		config.Config{HostShutdownBudget: time.Hour},
		server.Config,
		mgr,
		hostLifecycle,
		force,
		pollDone,
	); err != nil {
		t.Fatal(err)
	}
	if got := hostLifecycle.Snapshot().State; got != host.StateStopped {
		t.Fatalf("host state = %s, want stopped", got)
	}
}

func TestShutdownHostBudgetCapsProxyAndHTTPDrain(t *testing.T) {
	hostLifecycle := host.NewController()
	if err := hostLifecycle.Transition(host.StateServing); err != nil {
		t.Fatal(err)
	}

	requestStarted := make(chan struct{})
	handlerDone := make(chan struct{})
	server := httptest.NewServer(hostLifecycle.Admission(http.HandlerFunc(
		func(_ http.ResponseWriter, r *http.Request) {
			close(requestStarted)
			<-r.Context().Done()
			close(handlerDone)
		},
	)))
	t.Cleanup(server.Close)
	requestDone := make(chan struct{})
	go func() {
		response, _ := server.Client().Get(server.URL + "/v1")
		if response != nil {
			_ = response.Body.Close()
		}
		close(requestDone)
	}()

	select {
	case <-requestStarted:
	case <-time.After(time.Second):
		t.Fatal("proxy request did not start")
	}
	if err := hostLifecycle.Transition(host.StateDraining); err != nil {
		t.Fatal(err)
	}

	mgr := process.NewManager(config.Config{BasePort: 5000})
	force := make(chan struct{})
	pollDone := make(chan struct{})
	close(pollDone)
	budget := 50 * time.Millisecond
	started := time.Now()

	if err := shutdownHost(
		config.Config{HostShutdownBudget: budget},
		server.Config,
		mgr,
		hostLifecycle,
		force,
		pollDone,
	); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("shutdown took %s, want one bounded deadline", elapsed)
	}
	if got := hostLifecycle.Snapshot().State; got != host.StateStopped {
		t.Fatalf("host state = %s, want stopped", got)
	}
	select {
	case <-handlerDone:
	case <-time.After(time.Second):
		t.Fatal("HTTP handler survived shutdown escalation")
	}
	select {
	case <-requestDone:
	case <-time.After(time.Second):
		t.Fatal("proxy request did not return after shutdown")
	}
}

func TestShutdownHostContinuesWhenPollWorkerDoesNotUnwind(t *testing.T) {
	hostLifecycle := host.NewController()
	if err := hostLifecycle.Transition(host.StateDraining); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(server.Close)
	mgr := newFakeHostShutdownManager()
	force := make(chan struct{})
	pollDone := make(chan struct{})
	t.Cleanup(func() { close(pollDone) })

	result := make(chan error, 1)
	go func() {
		result <- shutdownHost(
			config.Config{HostShutdownBudget: 3 * time.Second},
			server.Config,
			mgr,
			hostLifecycle,
			force,
			pollDone,
		)
	}()

	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown remained blocked on the poll worker")
	}
	assertCallOrder(
		t,
		mgr.callLog(),
		"request_children_drain",
		"wait_children_idle",
		"shutdown",
	)
	if got := hostLifecycle.Snapshot().State; got != host.StateStopped {
		t.Fatalf("host state = %s, want stopped", got)
	}
}

func TestShutdownHostForceDoesNotWaitForPollWorker(t *testing.T) {
	hostLifecycle := host.NewController()
	if err := hostLifecycle.Transition(host.StateDraining); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(server.Close)
	mgr := newFakeHostShutdownManager()
	mgr.shutdown = func(ctx context.Context) error {
		select {
		case <-mgr.forceCalled:
			return ctx.Err()
		case <-time.After(time.Second):
			return errors.New("shutdown escalation did not force child stop")
		}
	}
	force := make(chan struct{})
	close(force)
	pollDone := make(chan struct{})
	t.Cleanup(func() { close(pollDone) })

	result := make(chan error, 1)
	go func() {
		result <- shutdownHost(
			config.Config{HostShutdownBudget: time.Hour},
			server.Config,
			mgr,
			hostLifecycle,
			force,
			pollDone,
		)
	}()

	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("forced shutdown remained blocked on the poll worker")
	}
	calls := mgr.callLog()
	if strings.Contains(calls, "request_children_drain") ||
		strings.Contains(calls, "wait_children_idle") {
		t.Fatalf("forced shutdown attempted graceful child drain:\n%s", calls)
	}
	if !strings.Contains(calls, "force_stop_children") {
		t.Fatalf("forced shutdown did not force child stop:\n%s", calls)
	}
	if got := hostLifecycle.Snapshot().State; got != host.StateStopped {
		t.Fatalf("host state = %s, want stopped", got)
	}
}

func TestPollWorkerUnwindBudgetReservesShutdownTime(t *testing.T) {
	tests := []struct {
		name   string
		budget time.Duration
		want   time.Duration
	}{
		{
			name:   "production budget is capped",
			budget: config.DefaultHostShutdownBudget,
			want:   maxPollWorkerUnwindWait,
		},
		{
			name:   "short budget keeps ninety percent",
			budget: 20 * time.Second,
			want:   2 * time.Second,
		},
		{
			name:   "sub-tick budget reserves no wait",
			budget: time.Nanosecond,
			want:   0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := pollWorkerUnwindBudget(tt.budget); got != tt.want {
				t.Fatalf(
					"pollWorkerUnwindBudget(%s) = %s, want %s",
					tt.budget,
					got,
					tt.want,
				)
			}
		})
	}
}

func TestWaitForPollWorkerReportsWaitEndReason(t *testing.T) {
	pollDone := make(chan struct{})

	t.Run("forced cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		err := waitForPollWorker(ctx, pollDone, time.Second)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("wait error = %v, want context.Canceled", err)
		}
	})

	t.Run("shutdown deadline", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
		defer cancel()
		err := waitForPollWorker(ctx, pollDone, time.Second)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("wait error = %v, want context.DeadlineExceeded", err)
		}
	})

	t.Run("unwind allowance elapsed", func(t *testing.T) {
		err := waitForPollWorker(
			context.Background(),
			pollDone,
			10*time.Millisecond,
		)
		if !errors.Is(err, errPollWorkerUnwindTimeout) {
			t.Fatalf("wait error = %v, want poll worker timeout", err)
		}
	})

	t.Run("no unwind allowance", func(t *testing.T) {
		err := waitForPollWorker(context.Background(), pollDone, 0)
		if !errors.Is(err, errPollWorkerUnwindTimeout) {
			t.Fatalf("wait error = %v, want poll worker timeout", err)
		}
	})
}

func TestShutdownHostWaitsForChildIdleBeforeManagerShutdown(t *testing.T) {
	hostLifecycle := host.NewController()
	if err := hostLifecycle.Transition(host.StateServing); err != nil {
		t.Fatal(err)
	}
	if err := hostLifecycle.Transition(host.StateDraining); err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(server.Close)
	waitStarted := make(chan struct{})
	childIdle := make(chan struct{})
	mgr := newFakeHostShutdownManager()
	mgr.waitChildrenIdle = func(ctx context.Context) error {
		close(waitStarted)
		select {
		case <-childIdle:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	force := make(chan struct{})
	pollDone := make(chan struct{})
	close(pollDone)

	result := make(chan error, 1)
	go func() {
		result <- shutdownHost(
			config.Config{HostShutdownBudget: time.Second},
			server.Config,
			mgr,
			hostLifecycle,
			force,
			pollDone,
		)
	}()

	select {
	case <-waitStarted:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not start waiting for child idle")
	}
	if calls := mgr.callLog(); strings.Contains(calls, "\nshutdown") {
		t.Fatalf("manager shutdown started before child became idle:\n%s", calls)
	}
	if got := hostLifecycle.Snapshot().State; got != host.StateDraining {
		t.Fatalf("host state = %s while child is busy, want draining", got)
	}

	close(childIdle)
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown did not finish after child became idle")
	}
	assertCallOrder(
		t,
		mgr.callLog(),
		"request_children_drain",
		"wait_children_idle",
		"shutdown",
	)
	if got := hostLifecycle.Snapshot().State; got != host.StateStopped {
		t.Fatalf("host state = %s, want stopped", got)
	}
}

func TestShutdownHostChildIdleTimeoutForcesAndContinues(t *testing.T) {
	hostLifecycle := host.NewController()
	if err := hostLifecycle.Transition(host.StateServing); err != nil {
		t.Fatal(err)
	}
	if err := hostLifecycle.Transition(host.StateDraining); err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(server.Close)
	mgr := newFakeHostShutdownManager()
	mgr.waitChildrenIdle = func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	}
	mgr.shutdown = func(ctx context.Context) error {
		select {
		case <-mgr.forceCalled:
			return ctx.Err()
		case <-time.After(time.Second):
			t.Fatal("shutdown escalation did not force child stop")
			return nil
		}
	}
	force := make(chan struct{})
	pollDone := make(chan struct{})
	close(pollDone)

	if err := shutdownHost(
		config.Config{HostShutdownBudget: 30 * time.Millisecond},
		server.Config,
		mgr,
		hostLifecycle,
		force,
		pollDone,
	); err != nil {
		t.Fatal(err)
	}
	calls := mgr.callLog()
	assertCallOrder(
		t,
		calls,
		"request_children_drain",
		"wait_children_idle",
		"shutdown",
	)
	if !strings.Contains(calls, "force_stop_children") {
		t.Fatalf("child idle timeout did not force child stop:\n%s", calls)
	}
	if got := hostLifecycle.Snapshot().State; got != host.StateStopped {
		t.Fatalf("host state = %s, want stopped", got)
	}
}

func TestVersiondReadyRequiresServingAndFullReconciliation(t *testing.T) {
	status := host.Snapshot{State: host.StateServing, Accepting: true}
	conditions := process.Conditions{Available: true, Reconciled: true}
	if !versiondReady(status, conditions) {
		t.Fatal("fully reconciled serving host is not ready")
	}

	conditions.Progressing = true
	if versiondReady(status, conditions) {
		t.Fatal("progressing host is ready")
	}
	conditions.Progressing = false
	conditions.Degraded = true
	if versiondReady(status, conditions) {
		t.Fatal("degraded host is ready")
	}
	conditions.Degraded = false
	status.State = host.StateDraining
	status.Accepting = false
	if versiondReady(status, conditions) {
		t.Fatal("draining host is ready")
	}
}

func TestReadinessIsOnlyServedOnAdminListener(t *testing.T) {
	hostLifecycle := host.NewController()
	mgr := process.NewManager(config.Config{BasePort: 5000})

	publicResponse := httptest.NewRecorder()
	publicHandler(mgr, hostLifecycle).ServeHTTP(
		publicResponse,
		httptest.NewRequest(http.MethodGet, "/ready", nil),
	)
	if publicResponse.Code != http.StatusNotFound {
		t.Fatalf(
			"public /ready status = %d, want %d",
			publicResponse.Code,
			http.StatusNotFound,
		)
	}

	adminResponse := httptest.NewRecorder()
	adminHandler(hostLifecycle, mgr).ServeHTTP(
		adminResponse,
		httptest.NewRequest(http.MethodGet, "/ready", nil),
	)
	if adminResponse.Code != http.StatusServiceUnavailable {
		t.Fatalf(
			"admin /ready status = %d, want %d",
			adminResponse.Code,
			http.StatusServiceUnavailable,
		)
	}
	if got := adminResponse.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("admin /ready Cache-Control = %q, want no-store", got)
	}
}

func TestHTTPServerGroupShutsDownEveryListener(t *testing.T) {
	first := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(first.Close)
	second := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(second.Close)

	servers := httpServerGroup{first.Config, second.Config}
	if err := servers.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}

	for _, server := range []*httptest.Server{first, second} {
		response, err := server.Client().Get(server.URL)
		if err == nil {
			_ = response.Body.Close()
			t.Fatalf("HTTP server %s still accepts requests", server.URL)
		}
	}
}

func TestCancelOnSignalCancelsContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	signal := make(chan struct{})
	stop := cancelOnSignal(ctx, cancel, signal)
	defer stop()
	close(signal)
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("force signal did not cancel context")
	}
}

func TestShouldForceShutdown(t *testing.T) {
	tests := []struct {
		name string
		sig  os.Signal
		want bool
	}{
		{name: "duplicate SIGTERM", sig: syscall.SIGTERM, want: false},
		{name: "SIGINT escalation", sig: syscall.SIGINT, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldForceShutdown(tt.sig); got != tt.want {
				t.Fatalf("shouldForceShutdown(%s) = %t, want %t", tt.sig, got, tt.want)
			}
		})
	}
}

func TestWatchForceSignalsForcesOnSIGINT(t *testing.T) {
	signals := make(chan os.Signal)
	shutdownDone := make(chan struct{})
	force := make(chan struct{})
	go watchForceSignals(signals, shutdownDone, force)

	signals <- syscall.SIGTERM
	sentSIGINT := make(chan struct{})
	go func() {
		signals <- syscall.SIGINT
		close(sentSIGINT)
	}()
	select {
	case <-sentSIGINT:
	case <-time.After(time.Second):
		t.Fatal("watcher stopped after duplicate SIGTERM")
	}
	select {
	case <-force:
	case <-time.After(time.Second):
		t.Fatal("SIGINT did not force host shutdown")
	}
}

func assertCallOrder(t *testing.T, calls string, fragments ...string) {
	t.Helper()
	position := -1
	for _, fragment := range fragments {
		next := strings.Index(calls[position+1:], fragment)
		if next < 0 {
			t.Fatalf("call %q missing or out of order:\n%s", fragment, calls)
		}
		position += next + 1
	}
}
