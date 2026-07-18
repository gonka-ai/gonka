package process

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"versioned/internal/config"
	"versioned/internal/oracle"
)

func TestGenerationLifecycle(t *testing.T) {
	c := &child{status: statusPreparing}
	for _, state := range []generationState{
		statusStarting,
		statusRunning,
		statusRetiring,
		statusDraining,
		statusStopping,
		statusStopped,
	} {
		if !transitionGenerationLocked(c, state) {
			t.Fatalf("transition to %s rejected from %s", state, c.status)
		}
	}
	if validGenerationTransition(c.status, statusRunning) {
		t.Fatal("terminal generation returned to running")
	}
}

func TestGenerationHealthStatusKeepsRetirementOutOfRoutes(t *testing.T) {
	for _, state := range []generationState{statusRetiring, statusDraining, statusStopping} {
		if got := healthGenerationStatus(state); got != "draining" {
			t.Fatalf("health status for %s = %q, want draining", state, got)
		}
	}
}

func TestConditionsReportPartialConvergenceAsDegraded(t *testing.T) {
	m := NewManager(config.Config{BasePort: 5000})
	m.mu.Lock()
	m.conditions = Conditions{Desired: 2, Reconciled: true}
	m.processes["v1"] = &child{status: statusRunning}
	m.processes["v2"] = &child{status: statusStarting}
	m.mu.Unlock()

	conditions := m.Conditions()
	if !conditions.Available || conditions.Reconciled || !conditions.Degraded {
		t.Fatalf("unexpected partial conditions: %+v", conditions)
	}
	if conditions.ReconcileError == "" {
		t.Fatal("partial convergence has no diagnostic")
	}

	m.mu.Lock()
	m.processes["v2"].status = statusRunning
	m.mu.Unlock()
	conditions = m.Conditions()
	if !conditions.Available || !conditions.Reconciled || conditions.Degraded {
		t.Fatalf("unexpected converged conditions: %+v", conditions)
	}
}

func TestConditionsKeepServingWhenReconcileSourceFails(t *testing.T) {
	m := NewManager(config.Config{BasePort: 5000})
	m.mu.Lock()
	m.conditions = Conditions{Desired: 1, Reconciled: true}
	m.processes["v1"] = &child{status: statusRunning}
	m.mu.Unlock()
	m.ReportReconcileError(errors.New("oracle unavailable"))

	conditions := m.Conditions()
	if !conditions.Available || conditions.Reconciled || !conditions.Degraded {
		t.Fatalf("unexpected source failure conditions: %+v", conditions)
	}
	if conditions.ReconcileError != "oracle unavailable" {
		t.Fatalf("reconcile error = %q", conditions.ReconcileError)
	}
}

func TestAvailabilityEventRearmsAfterRouteLoss(t *testing.T) {
	m := NewManager(config.Config{BasePort: 5000})
	addRunning := func() {
		m.mu.Lock()
		m.processes["v1"] = &child{
			version: oracle.Version{Name: "v1"},
			port:    5000,
			status:  statusRunning,
		}
		m.rebuildRoutes()
		m.mu.Unlock()
	}
	remove := func() {
		m.mu.Lock()
		delete(m.processes, "v1")
		m.rebuildRoutes()
		m.mu.Unlock()
	}

	addRunning()
	select {
	case <-m.Available():
	case <-time.After(time.Second):
		t.Fatal("initial availability event was not published")
	}
	remove()
	addRunning()
	select {
	case <-m.Available():
	case <-time.After(time.Second):
		t.Fatal("availability event was not rearmed")
	}
}

func TestHostDrainCancelsBlockedReconcileOperation(t *testing.T) {
	dir := t.TempDir()
	overridePath := filepath.Join(dir, "override")
	if err := os.WriteFile(overridePath, []byte("replacement"), 0o755); err != nil {
		t.Fatal(err)
	}

	stopCalled := make(chan struct{})
	existing := &child{
		version:       oracle.Version{Name: "v1"},
		archiveSHA256: "old",
		binPath:       filepath.Join(dir, "installed"),
		status:        statusRunning,
		done:          make(chan struct{}),
		stop:          func() { close(stopCalled) },
		restart:       true,
	}
	m := NewManager(config.Config{
		BinDir:    filepath.Join(dir, "bin"),
		DataDir:   filepath.Join(dir, "data"),
		BasePort:  5000,
		Overrides: map[string]string{"v1": overridePath},
	})
	m.mu.Lock()
	m.processes["v1"] = existing
	m.children[existing] = struct{}{}
	m.mu.Unlock()

	errCh := make(chan error, 1)
	go func() {
		errCh <- m.Reconcile(context.Background(), []oracle.Version{{Name: "v1"}})
	}()
	select {
	case <-stopCalled:
	case <-time.After(time.Second):
		t.Fatal("override reconcile did not enter child wait")
	}

	m.BeginHostDrain()
	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) && !errors.Is(err, ErrHostDraining) {
			t.Fatalf("reconcile error = %v, want cancellation", err)
		}
	case <-time.After(time.Second):
		t.Fatal("host drain did not cancel blocked reconcile")
	}
}

func TestPreflightHonorsCallerCancellation(t *testing.T) {
	binPath := filepath.Join(t.TempDir(), "probe")
	if err := os.WriteFile(binPath, []byte("#!/bin/sh\nexec sleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := preflightChildWithAdminProbeContext(ctx, binPath, "v1", true)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("preflight error = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("canceled preflight took %s", elapsed)
	}
}
