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

func TestVersiondReadyTracksTrafficCapacityNotConvergence(t *testing.T) {
	status := host.Snapshot{State: host.StateServing, Accepting: true, Ready: true}
	conditions := process.Conditions{Available: true, Converged: true}
	if !versiondReady(status, conditions) {
		t.Fatal("serving host with a running child is not ready")
	}

	// A routine same-name SHA bump makes every host progress at once. Evicting
	// them all would take the pool down, so progressing must stay ready.
	conditions.Progressing = true
	if !versiondReady(status, conditions) {
		t.Fatal("progressing host lost readiness; the whole pool would drop out")
	}
	conditions.Progressing = false

	conditions.Converged = false
	if versiondReady(status, conditions) {
		t.Fatal("host that never converged is ready")
	}
	conditions.Converged = true

	// Every versiond reads the same oracle, so a failed reconcile is almost
	// always failing everywhere at once. Gating on it would turn an oracle blip
	// into an empty pool while every child is still serving.
	conditions.Degraded = true
	if !versiondReady(status, conditions) {
		t.Fatal("a reconcile failure took a serving host out of rotation; " +
			"one oracle hiccup would empty the pool")
	}
	conditions.Degraded = false

	conditions.Available = false
	if versiondReady(status, conditions) {
		t.Fatal("host without a running child is ready")
	}
	conditions.Available = true

	// announcing still accepts work but must already report unready.
	status.State = host.StateAnnouncing
	status.Ready = false
	if versiondReady(status, conditions) {
		t.Fatal("announcing host is ready")
	}
}

type fakeVersionServer map[string]bool

func (f fakeVersionServer) ServesVersion(name string) bool { return f[name] }

// The question a balancer actually has is about the version it is about to
// route, and a host missing one version must keep serving the others.
func TestVersiondReadyForVersionAnswersPerVersion(t *testing.T) {
	status := host.Snapshot{State: host.StateServing, Accepting: true, Ready: true}
	serves := fakeVersionServer{"v4": true}

	if !versiondReadyForVersion(status, serves, "v4") {
		t.Fatal("host with a running v4 route is not ready for v4")
	}
	if versiondReadyForVersion(status, serves, "v5") {
		t.Fatal("host without a v5 route is ready for v5")
	}

	// No convergence latch and no view of the desired set: a host that has never
	// run its full set still serves what it does have.
	if !versiondReadyForVersion(status, serves, "v4") {
		t.Fatal("per-version readiness should not depend on overall convergence")
	}

	// Draining has to take every version out at once, or the announce window
	// would never empty the host.
	status.State = host.StateAnnouncing
	status.Ready = false
	if versiondReadyForVersion(status, serves, "v4") {
		t.Fatal("announcing host is still ready for v4")
	}
}

func TestReadinessAnswersTheVersionItWasAskedAbout(t *testing.T) {
	hostLifecycle := host.NewController()
	mgr := process.NewManager(config.Config{BasePort: 5000})
	if err := hostLifecycle.Transition(host.StateServing); err != nil {
		t.Fatalf("transition to serving: %v", err)
	}
	srv := httptest.NewServer(readinessHandler(mgr, hostLifecycle))
	defer srv.Close()

	// No child runs, so every version is unserved — including the unqualified
	// host-level question.
	for _, path := range []string{"/readyz", "/readyz?version=v4"} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("GET %s = %d, want 503", path, resp.StatusCode)
		}
	}
}

func TestReadinessIsServedOnTheTrafficListener(t *testing.T) {
	hostLifecycle := host.NewController()
	mgr := process.NewManager(config.Config{BasePort: 5000})

	response := httptest.NewRecorder()
	publicHandler(mgr, hostLifecycle).ServeHTTP(
		response,
		httptest.NewRequest(http.MethodGet, "/readyz", nil),
	)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf(
			"/readyz status = %d, want %d",
			response.Code,
			http.StatusServiceUnavailable,
		)
	}
	if got := response.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("/readyz Cache-Control = %q, want no-store", got)
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
