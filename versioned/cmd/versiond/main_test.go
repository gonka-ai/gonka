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
		BasePort:         5000,
		HostDrainTimeout: time.Second,
		DrainKillGrace:   time.Second,
	})
	force := make(chan struct{})

	if err := shutdownHost(
		config.Config{HostDrainTimeout: time.Second},
		server.Config,
		mgr,
		hostLifecycle,
		force,
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

	if err := shutdownHost(
		config.Config{HostDrainTimeout: time.Hour},
		server.Config,
		mgr,
		hostLifecycle,
		force,
	); err != nil {
		t.Fatal(err)
	}
	if got := hostLifecycle.Snapshot().State; got != host.StateStopped {
		t.Fatalf("host state = %s, want stopped", got)
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
