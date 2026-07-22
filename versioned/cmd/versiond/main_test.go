package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"syscall"
	"testing"
	"time"

	"versioned/internal/config"
	"versioned/internal/host"
	"versioned/internal/process"
)

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

func TestWatchForceSignalsIgnoresDuplicateSIGTERM(t *testing.T) {
	signals := make(chan os.Signal)
	shutdownDone := make(chan struct{})
	force := make(chan struct{})
	go watchForceSignals(signals, shutdownDone, force)

	signals <- syscall.SIGTERM
	select {
	case <-force:
		t.Fatal("duplicate SIGTERM forced host shutdown")
	default:
	}

	signals <- syscall.SIGINT
	select {
	case <-force:
	case <-time.After(time.Second):
		t.Fatal("second SIGINT did not force host shutdown")
	}
}
