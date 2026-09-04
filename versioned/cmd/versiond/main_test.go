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
	shutdownGrace    time.Duration
	forceCalled      chan struct{}
	forceOnce        sync.Once
}

func newFakeHostShutdownManager() *fakeHostShutdownManager {
	return &fakeHostShutdownManager{forceCalled: make(chan struct{})}
}

type fakeHostDrainManager struct {
	beginOnce sync.Once
	began     chan struct{}
}

func newFakeHostDrainManager() *fakeHostDrainManager {
	return &fakeHostDrainManager{began: make(chan struct{})}
}

func (m *fakeHostDrainManager) BeginHostDrain() {
	m.beginOnce.Do(func() { close(m.began) })
}

type fakeHostAvailabilityProvider struct {
	available  chan struct{}
	conditions process.Conditions
}

type blockingHostDrainManager struct {
	began   chan struct{}
	release chan struct{}
}

func (m *blockingHostDrainManager) BeginHostDrain() {
	close(m.began)
	<-m.release
}

type racingHostHTTPServer struct {
	shutdownStarted chan struct{}
	release         <-chan struct{}
	startOnce       sync.Once
}

func (s *racingHostHTTPServer) Shutdown(ctx context.Context) error {
	s.startOnce.Do(func() { close(s.shutdownStarted) })
	select {
	case <-s.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *racingHostHTTPServer) Close() error {
	return nil
}

func (p *fakeHostAvailabilityProvider) Available() <-chan struct{} {
	return p.available
}

func (p *fakeHostAvailabilityProvider) Conditions() process.Conditions {
	return p.conditions
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

func (m *fakeHostShutdownManager) ShutdownGrace() time.Duration {
	return m.shutdownGrace
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

func TestBeginHostDrainAnnouncesBeforeClosingAdmission(t *testing.T) {
	hostLifecycle := host.NewController()
	if err := hostLifecycle.Transition(host.StateServing); err != nil {
		t.Fatal(err)
	}
	mgr := newFakeHostDrainManager()
	pollCtx, cancelPoll := context.WithCancel(context.Background())
	force := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		result <- beginHostDrain(
			time.Hour,
			mgr,
			hostLifecycle,
			force,
			cancelPoll,
		)
	}()

	select {
	case <-mgr.began:
	case <-time.After(time.Second):
		t.Fatal("manager drain barrier was not raised")
	}
	select {
	case <-pollCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("poll loop was not canceled before the announce wait")
	}

	snapshot := hostLifecycle.Snapshot()
	if snapshot.State != host.StateAnnouncing || snapshot.Ready || !snapshot.Accepting {
		t.Fatalf("announce snapshot = %+v, want unready and accepting", snapshot)
	}
	response := httptest.NewRecorder()
	hostLifecycle.Admission(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1", nil))
	if response.Code != http.StatusNoContent {
		t.Fatalf("announce admission status = %d, want %d", response.Code, http.StatusNoContent)
	}

	select {
	case err := <-result:
		t.Fatalf("announce window ended early: %v", err)
	default:
	}
	close(force)
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("host did not enter draining after the announce window")
	}
	snapshot = hostLifecycle.Snapshot()
	if snapshot.State != host.StateDraining || snapshot.Accepting {
		t.Fatalf("post-announce snapshot = %+v, want draining and closed admission", snapshot)
	}
}

func TestBeginHostDrainSkipsAnnouncementBeforeFirstServing(t *testing.T) {
	hostLifecycle := host.NewController()
	mgr := newFakeHostDrainManager()
	started := time.Now()

	err := beginHostDrain(
		time.Hour,
		mgr,
		hostLifecycle,
		make(chan struct{}),
		func() {},
	)
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("never-serving host waited through announce window: %s", elapsed)
	}
	if got := hostLifecycle.Snapshot().State; got != host.StateDraining {
		t.Fatalf("host state = %s, want draining", got)
	}
}

func TestBeginHostDrainForceCutsAnnouncementShort(t *testing.T) {
	hostLifecycle := host.NewController()
	if err := hostLifecycle.Transition(host.StateServing); err != nil {
		t.Fatal(err)
	}
	mgr := newFakeHostDrainManager()
	force := make(chan struct{})
	close(force)
	started := time.Now()

	err := beginHostDrain(time.Hour, mgr, hostLifecycle, force, func() {})
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("force did not cut announce window short: %s", elapsed)
	}
}

func TestPromoteHostWhenAvailableHonorsDrainState(t *testing.T) {
	t.Run("starting host becomes serving", func(t *testing.T) {
		provider := &fakeHostAvailabilityProvider{
			available:  make(chan struct{}, 1),
			conditions: process.Conditions{Available: true},
		}
		provider.available <- struct{}{}
		hostLifecycle := host.NewController()

		promoteHostWhenAvailable(context.Background(), provider, hostLifecycle)

		if got := hostLifecycle.Snapshot().State; got != host.StateServing {
			t.Fatalf("host state = %s, want serving", got)
		}
	})

	t.Run("draining host cannot be reopened", func(t *testing.T) {
		provider := &fakeHostAvailabilityProvider{
			available:  make(chan struct{}, 1),
			conditions: process.Conditions{Available: true},
		}
		provider.available <- struct{}{}
		hostLifecycle := host.NewController()
		if err := hostLifecycle.Transition(host.StateDraining); err != nil {
			t.Fatal(err)
		}

		promoteHostWhenAvailable(context.Background(), provider, hostLifecycle)

		if got := hostLifecycle.Snapshot().State; got != host.StateDraining {
			t.Fatalf("host state = %s, want draining", got)
		}
	})
}

func TestBeginHostDrainCannotRaceAvailabilityPromotion(t *testing.T) {
	hostLifecycle := host.NewController()
	mgr := &blockingHostDrainManager{
		began:   make(chan struct{}),
		release: make(chan struct{}),
	}
	t.Cleanup(func() {
		select {
		case <-mgr.release:
		default:
			close(mgr.release)
		}
	})
	drainDone := make(chan error, 1)
	go func() {
		drainDone <- beginHostDrain(
			time.Hour,
			mgr,
			hostLifecycle,
			make(chan struct{}),
			func() {},
		)
	}()

	select {
	case <-mgr.began:
	case <-time.After(time.Second):
		t.Fatal("host drain did not reach the manager barrier")
	}
	if got := hostLifecycle.Snapshot().State; got != host.StateDraining {
		t.Fatalf("host state at manager barrier = %s, want draining", got)
	}

	provider := &fakeHostAvailabilityProvider{
		available:  make(chan struct{}, 1),
		conditions: process.Conditions{Available: true},
	}
	provider.available <- struct{}{}
	promoteHostWhenAvailable(context.Background(), provider, hostLifecycle)
	if got := hostLifecycle.Snapshot().State; got != host.StateDraining {
		t.Fatalf("availability promotion reopened draining host as %s", got)
	}

	close(mgr.release)
	if err := <-drainDone; err != nil {
		t.Fatal(err)
	}
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
		server.Config,
		mgr,
		hostLifecycle,
		force,
		pollDone,
		time.Now().Add(time.Second),
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
		server.Config,
		mgr,
		hostLifecycle,
		force,
		pollDone,
		time.Now().Add(time.Hour),
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
	releaseHandler := make(chan struct{})
	server := httptest.NewServer(hostLifecycle.Admission(http.HandlerFunc(
		func(_ http.ResponseWriter, r *http.Request) {
			close(requestStarted)
			select {
			case <-r.Context().Done():
			case <-releaseHandler:
			}
			close(handlerDone)
		},
	)))
	t.Cleanup(func() {
		close(releaseHandler)
		server.Close()
	})
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
		server.Config,
		mgr,
		hostLifecycle,
		force,
		pollDone,
		time.Now().Add(budget),
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
			server.Config,
			mgr,
			hostLifecycle,
			force,
			pollDone,
			time.Now().Add(3*time.Second),
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
			server.Config,
			mgr,
			hostLifecycle,
			force,
			pollDone,
			time.Now().Add(time.Hour),
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

func TestShutdownHostForceRaceAlwaysReachesStopped(t *testing.T) {
	const iterations = 64
	for iteration := range iterations {
		hostLifecycle := host.NewController()
		if err := hostLifecycle.Transition(host.StateDraining); err != nil {
			t.Fatal(err)
		}
		release := make(chan struct{})
		server := &racingHostHTTPServer{
			shutdownStarted: make(chan struct{}),
			release:         release,
		}
		managerStarted := make(chan struct{})
		mgr := newFakeHostShutdownManager()
		mgr.shutdown = func(ctx context.Context) error {
			close(managerStarted)
			select {
			case <-release:
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
				server,
				mgr,
				hostLifecycle,
				force,
				pollDone,
				time.Now().Add(time.Second),
			)
		}()

		select {
		case <-server.shutdownStarted:
		case <-time.After(time.Second):
			t.Fatalf("iteration %d HTTP shutdown did not start", iteration)
		}
		select {
		case <-managerStarted:
		case <-time.After(time.Second):
			t.Fatalf("iteration %d manager shutdown did not start", iteration)
		}
		start := make(chan struct{})
		var racers sync.WaitGroup
		racers.Add(2)
		go func() {
			defer racers.Done()
			<-start
			close(force)
		}()
		go func() {
			defer racers.Done()
			<-start
			close(release)
		}()
		close(start)
		racers.Wait()

		select {
		case err := <-result:
			if err != nil {
				t.Fatalf("iteration %d shutdown: %v", iteration, err)
			}
		case <-time.After(time.Second):
			t.Fatalf("iteration %d shutdown did not finish", iteration)
		}
		snapshot := hostLifecycle.Snapshot()
		if snapshot.State != host.StateStopped || snapshot.Inflight != 0 {
			t.Fatalf("iteration %d final snapshot = %+v", iteration, snapshot)
		}
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
			server.Config,
			mgr,
			hostLifecycle,
			force,
			pollDone,
			time.Now().Add(time.Second),
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

func TestShutdownHostReservesChildTerminationGrace(t *testing.T) {
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
	mgr.shutdownGrace = 10 * time.Minute
	drainDeadline := make(chan time.Time, 1)
	shutdownDeadline := make(chan time.Time, 1)
	mgr.waitChildrenIdle = func(ctx context.Context) error {
		deadline, ok := ctx.Deadline()
		if !ok {
			t.Fatal("child drain context has no deadline")
		}
		drainDeadline <- deadline
		return nil
	}
	mgr.shutdown = func(ctx context.Context) error {
		deadline, ok := ctx.Deadline()
		if !ok {
			t.Fatal("manager shutdown context has no deadline")
		}
		shutdownDeadline <- deadline
		return nil
	}
	force := make(chan struct{})
	pollDone := make(chan struct{})
	close(pollDone)
	outerDeadline := time.Now().Add(time.Hour)

	if err := shutdownHost(
		server.Config,
		mgr,
		hostLifecycle,
		force,
		pollDone,
		outerDeadline,
	); err != nil {
		t.Fatal(err)
	}
	if got := <-drainDeadline; !got.Equal(outerDeadline.Add(-mgr.shutdownGrace)) {
		t.Fatalf("drain deadline = %s, want %s", got, outerDeadline.Add(-mgr.shutdownGrace))
	}
	if got := <-shutdownDeadline; !got.Equal(outerDeadline) {
		t.Fatalf("shutdown deadline = %s, want %s", got, outerDeadline)
	}
}

// The absolute deadline is fixed when the shutdown signal arrives, before the
// announce window runs; shutdownHost must spend what remains of it, not restart
// the clock from the configured budget. A deadline that is already nearly gone
// escalates immediately, however generous the budget looks.
func TestShutdownHostHonoursTheDeadlineNotTheBudget(t *testing.T) {
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
			t.Fatal("an expired deadline did not force the shutdown; the budget restarted the clock")
			return nil
		}
	}
	force := make(chan struct{})
	pollDone := make(chan struct{})
	close(pollDone)

	if err := shutdownHost(
		server.Config,
		mgr,
		hostLifecycle,
		force,
		pollDone,
		time.Now().Add(30*time.Millisecond),
	); err != nil {
		t.Fatal(err)
	}
	if calls := mgr.callLog(); !strings.Contains(calls, "force_stop_children") {
		t.Fatalf("hour-long budget masked the expired deadline:\n%s", calls)
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
		server.Config,
		mgr,
		hostLifecycle,
		force,
		pollDone,
		time.Now().Add(30*time.Millisecond),
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
	conditions := process.Conditions{Available: true, Serving: true, Converged: true}
	if !versiondReady(status, conditions) {
		t.Fatal("serving host with a running child is not ready")
	}

	// A child process can exist while unable to take a request, for example when
	// its storage is unavailable. Available alone must not keep the host in the
	// pool.
	conditions.Serving = false
	if versiondReady(status, conditions) {
		t.Fatal("host whose children are running but not live-ready is ready")
	}
	conditions.Serving = true

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
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		for _, path := range []string{"/readyz", "/readyz?version=v4"} {
			req, err := http.NewRequest(method, srv.URL+path, nil)
			if err != nil {
				t.Fatal(err)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("%s %s: %v", method, path, err)
			}
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusServiceUnavailable {
				t.Fatalf("%s %s = %d, want 503", method, path, resp.StatusCode)
			}
		}
	}

	req, err := http.NewRequest(http.MethodPost, srv.URL+"/readyz", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST /readyz = %d, want 405", resp.StatusCode)
	}
	if got := resp.Header.Get("Allow"); got != "GET, HEAD" {
		t.Fatalf("POST /readyz Allow = %q, want GET, HEAD", got)
	}
}

func TestReadinessIsServedOnTheTrafficListener(t *testing.T) {
	hostLifecycle := host.NewController()
	mgr := process.NewManager(config.Config{BasePort: 5000})

	response := httptest.NewRecorder()
	publicHandler(mgr, hostLifecycle, nil).ServeHTTP(
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

type staticStorageIdentity string

func (s staticStorageIdentity) StorageIdentity(context.Context) (process.StorageProof, error) {
	return process.StorageProof{
		Identity: string(s),
		Children: 1,
		Snapshot: "snapshot-1",
		Targets: []process.StorageProofTarget{{
			Generation:                "1",
			Version:                   "v5",
			PoolMaxConnections:        4,
			ServerMaxConnections:      100,
			ServerReservedConnections: 3,
		}},
	}, nil
}

type unavailableStorageIdentity struct{}

func (unavailableStorageIdentity) StorageIdentity(context.Context) (process.StorageProof, error) {
	return process.StorageProof{}, errors.New("identity unavailable")
}

type staticStorageChallenge struct {
	proof process.StorageProof
	err   error
}

func (s staticStorageChallenge) StorageChallenge(context.Context, string, string, string, string) (process.StorageProof, error) {
	return s.proof, s.err
}

func TestStorageIdentityIsLocalOnly(t *testing.T) {
	handler := storageIdentityHandler(staticStorageIdentity("database-1"))

	external := httptest.NewRequest(http.MethodGet, "/internal/storage-identity", nil)
	external.RemoteAddr = "192.0.2.10:1234"
	externalResponse := httptest.NewRecorder()
	handler.ServeHTTP(externalResponse, external)
	if externalResponse.Code != http.StatusNotFound {
		t.Fatalf("external storage identity status = %d, want 404", externalResponse.Code)
	}

	local := httptest.NewRequest(http.MethodGet, "/internal/storage-identity", nil)
	local.RemoteAddr = "127.0.0.1:1234"
	localResponse := httptest.NewRecorder()
	handler.ServeHTTP(localResponse, local)
	if localResponse.Code != http.StatusOK {
		t.Fatalf("local storage identity status = %d, want 200", localResponse.Code)
	}
	if got := localResponse.Body.String(); got != "{\"identity\":\"database-1\",\"children\":1,\"snapshot\":\"snapshot-1\",\"targets\":[{\"generation\":\"1\",\"version\":\"v5\",\"pool_max_connections\":4,\"server_max_connections\":100,\"server_reserved_connections\":3}]}\n" {
		t.Fatalf("local storage identity response = %q", got)
	}

	ipv6 := httptest.NewRequest(http.MethodGet, "/internal/storage-identity", nil)
	ipv6.RemoteAddr = "[::1]:1234"
	ipv6Response := httptest.NewRecorder()
	handler.ServeHTTP(ipv6Response, ipv6)
	if ipv6Response.Code != http.StatusOK {
		t.Fatalf("IPv6 loopback storage identity status = %d, want 200", ipv6Response.Code)
	}

	post := httptest.NewRequest(http.MethodPost, "/internal/storage-identity", nil)
	post.RemoteAddr = "127.0.0.1:1234"
	postResponse := httptest.NewRecorder()
	handler.ServeHTTP(postResponse, post)
	if postResponse.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST storage identity status = %d, want 405", postResponse.Code)
	}
	if got := postResponse.Header().Get("Allow"); got != http.MethodGet {
		t.Fatalf("POST storage identity Allow = %q, want GET", got)
	}
}

func TestStorageIdentityUnavailable(t *testing.T) {
	tests := []struct {
		name   string
		reader storageIdentityReader
	}{
		{name: "nil interface", reader: nil},
		{name: "query failure", reader: unavailableStorageIdentity{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/internal/storage-identity", nil)
			request.RemoteAddr = "127.0.0.1:1234"
			response := httptest.NewRecorder()

			storageIdentityHandler(tt.reader).ServeHTTP(response, request)

			if response.Code != http.StatusServiceUnavailable {
				t.Fatalf("storage identity status = %d, want 503", response.Code)
			}
		})
	}
}

func TestStorageChallengeIsLocalAndValidated(t *testing.T) {
	runner := staticStorageChallenge{proof: process.StorageProof{
		Identity:   "database-1",
		Found:      true,
		Children:   2,
		Snapshot:   "snapshot-1",
		Generation: "generation-1",
	}}
	handler := storageChallengeHandler(runner)

	external := httptest.NewRequest(http.MethodPost, "/internal/storage-challenge",
		strings.NewReader(`{"operation":"write","nonce":"8aa1c262-ea39-43c2-928c-263e966cc9b4","snapshot":"snapshot-1","generation":"generation-1"}`))
	external.RemoteAddr = "192.0.2.10:1234"
	externalResponse := httptest.NewRecorder()
	handler.ServeHTTP(externalResponse, external)
	if externalResponse.Code != http.StatusNotFound {
		t.Fatalf("external storage challenge status = %d, want 404", externalResponse.Code)
	}

	local := httptest.NewRequest(http.MethodPost, "/internal/storage-challenge",
		strings.NewReader(`{"operation":"write","nonce":"8aa1c262-ea39-43c2-928c-263e966cc9b4","snapshot":"snapshot-1","generation":"generation-1"}`))
	local.RemoteAddr = "127.0.0.1:1234"
	localResponse := httptest.NewRecorder()
	handler.ServeHTTP(localResponse, local)
	if localResponse.Code != http.StatusOK {
		t.Fatalf("local storage challenge status = %d, want 200", localResponse.Code)
	}
	if got := localResponse.Body.String(); got != "{\"identity\":\"database-1\",\"found\":true,\"children\":2,\"snapshot\":\"snapshot-1\",\"generation\":\"generation-1\"}\n" {
		t.Fatalf("local storage challenge response = %q", got)
	}

	invalid := httptest.NewRequest(http.MethodPost, "/internal/storage-challenge",
		strings.NewReader(`{"operation":"delete","nonce":"x","snapshot":"snapshot-1","generation":"generation-1"}`))
	invalid.RemoteAddr = "127.0.0.1:1234"
	invalidResponse := httptest.NewRecorder()
	handler.ServeHTTP(invalidResponse, invalid)
	if invalidResponse.Code != http.StatusBadRequest {
		t.Fatalf("invalid storage challenge status = %d, want 400", invalidResponse.Code)
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

func TestWatchForceSignalsPrefersCompletedShutdown(t *testing.T) {
	signals := make(chan os.Signal, 1)
	signals <- syscall.SIGINT
	shutdownDone := make(chan struct{})
	close(shutdownDone)
	force := make(chan struct{})
	watcherDone := make(chan struct{})
	go func() {
		watchForceSignals(signals, shutdownDone, force)
		close(watcherDone)
	}()

	select {
	case <-watcherDone:
	case <-time.After(time.Second):
		t.Fatal("force watcher did not observe completed shutdown")
	}
	select {
	case <-force:
		t.Fatal("queued signal forced an already completed shutdown")
	default:
	}
}

func TestChannelClosed(t *testing.T) {
	open := make(chan struct{})
	if channelClosed(open) {
		t.Fatal("open channel reported closed")
	}
	close(open)
	if !channelClosed(open) {
		t.Fatal("closed channel reported open")
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
