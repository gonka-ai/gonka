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

func TestGenerationTransitionTable(t *testing.T) {
	states := []generationState{
		statusPreparing,
		statusStarting,
		statusRunning,
		statusRetiring,
		statusDraining,
		statusStopping,
		statusStopped,
		statusFailed,
	}
	allowed := map[generationState]map[generationState]bool{
		statusPreparing: {
			statusPreparing: true,
			statusStarting:  true,
			statusRetiring:  true,
			statusStopping:  true,
			statusStopped:   true,
			statusFailed:    true,
		},
		statusStarting: {
			statusStarting: true,
			statusRunning:  true,
			statusRetiring: true,
			statusStopping: true,
			statusStopped:  true,
			statusFailed:   true,
		},
		statusRunning: {
			statusRunning:  true,
			statusRetiring: true,
			statusStopping: true,
			statusStopped:  true,
			statusFailed:   true,
		},
		statusRetiring: {
			statusRetiring: true,
			statusDraining: true,
			statusStopping: true,
			statusStopped:  true,
		},
		statusDraining: {
			statusDraining: true,
			statusStopping: true,
			statusStopped:  true,
		},
		statusStopping: {
			statusStopping: true,
			statusStopped:  true,
		},
		statusStopped: {
			statusStopped: true,
		},
		statusFailed: {
			statusFailed:   true,
			statusStarting: true,
			statusRetiring: true,
			statusStopping: true,
			statusStopped:  true,
		},
	}

	for _, from := range states {
		for _, to := range states {
			if got, want := validGenerationTransition(from, to), allowed[from][to]; got != want {
				t.Errorf("validGenerationTransition(%s, %s) = %t, want %t", from, to, got, want)
			}
		}
	}
	if validGenerationTransition("unknown", "unknown") {
		t.Fatal("unknown generation state accepted a self-transition")
	}
}

func TestGenerationStateTableMetadata(t *testing.T) {
	tests := []struct {
		state        generationState
		phase        int
		healthStatus string
	}{
		{state: statusPreparing, phase: 0, healthStatus: "starting"},
		{state: statusStarting, phase: 1, healthStatus: "starting"},
		{state: statusRunning, phase: 2, healthStatus: "running"},
		{state: statusRetiring, phase: 3, healthStatus: "draining"},
		{state: statusDraining, phase: 4, healthStatus: "draining"},
		{state: statusStopping, phase: 5, healthStatus: "draining"},
		{state: statusStopped, phase: 6, healthStatus: "stopped"},
		{state: statusFailed, phase: unorderedGenerationPhase, healthStatus: "stopped"},
	}
	if len(generationStateTable) != len(tests) {
		t.Fatalf("generation state table has %d states, want %d", len(generationStateTable), len(tests))
	}
	for _, test := range tests {
		if got := generationPhase(test.state); got != test.phase {
			t.Errorf("generationPhase(%s) = %d, want %d", test.state, got, test.phase)
		}
		if got := healthGenerationStatus(test.state); got != test.healthStatus {
			t.Errorf(
				"healthGenerationStatus(%s) = %q, want %q",
				test.state,
				got,
				test.healthStatus,
			)
		}
	}
}

func TestGenerationStateTableTargetsAdvancePhase(t *testing.T) {
	for state, spec := range generationStateTable {
		for _, target := range spec.targets {
			targetSpec, ok := generationStateTable[target]
			if !ok {
				t.Errorf("%s targets unknown generation state %s", state, target)
				continue
			}
			if spec.phase == unorderedGenerationPhase ||
				targetSpec.phase == unorderedGenerationPhase {
				continue
			}
			if targetSpec.phase <= spec.phase {
				t.Errorf(
					"%s phase %d targets non-advancing %s phase %d",
					state,
					spec.phase,
					target,
					targetSpec.phase,
				)
			}
		}
	}
}

func TestGenerationTransitionTreatsLaterPhaseAsAlreadySatisfied(t *testing.T) {
	c := &child{status: statusDraining}
	if !transitionGenerationLocked(c, statusRetiring) {
		t.Fatal("stale retirement transition was rejected")
	}
	if c.status != statusDraining {
		t.Fatalf("stale transition moved generation back to %s", c.status)
	}
}

func TestGenerationHealthStatusKeepsRetirementOutOfRoutes(t *testing.T) {
	for _, state := range []generationState{statusRetiring, statusDraining, statusStopping} {
		if got := healthGenerationStatus(state); got != "draining" {
			t.Fatalf("health status for %s = %q, want draining", state, got)
		}
	}
}

func TestConditionsReportPartialConvergenceAsProgressing(t *testing.T) {
	m := NewManager(config.Config{BasePort: 5000})
	m.mu.Lock()
	m.conditions = Conditions{Desired: 2, Reconciled: true}
	m.processes["v1"] = &child{status: statusRunning}
	m.processes["v2"] = &child{status: statusStarting}
	m.mu.Unlock()

	conditions := m.Conditions()
	if !conditions.Available || conditions.Reconciled ||
		!conditions.Progressing || conditions.Degraded {
		t.Fatalf("unexpected partial conditions: %+v", conditions)
	}
	if conditions.ReconcileError != "" {
		t.Fatalf("expected no failure during progress, got %q", conditions.ReconcileError)
	}

	m.mu.Lock()
	m.processes["v2"].status = statusRunning
	m.mu.Unlock()
	conditions = m.Conditions()
	if !conditions.Available || !conditions.Reconciled ||
		conditions.Progressing || conditions.Degraded {
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
	// The child is still running, so the host can still serve. Every versiond
	// reads the same oracle, so retracting convergence here would take the whole
	// pool out of rotation over one unreachable control plane.
	if !conditions.Converged {
		t.Fatalf("a failed oracle read retracted convergence: %+v", conditions)
	}
}

// A new SHA that will not download reaches Degraded by a different route than an
// unreachable oracle: through the reconcile result rather than ReportReconcileError.
// The old child is deliberately kept running (see downloadAndSwap), so the host
// can still serve, and the conditions a balancer consumes must say so — this
// failure arrives on every host at once, since they all read the same archive.
func TestConditionsKeepServingWhenAnUpdateFailsToInstall(t *testing.T) {
	m := NewManager(config.Config{BasePort: 5000})
	m.mu.Lock()
	m.conditions = Conditions{Desired: 1, Reconciled: true}
	m.processes["v1"] = &child{status: statusRunning}
	m.mu.Unlock()
	if !m.Conditions().Converged {
		t.Fatal("host should have converged on its running child")
	}

	m.recordReconcileResult(1, errors.New("download or start v2: archive sha mismatch"))

	conditions := m.Conditions()
	if !conditions.Available {
		t.Fatalf("the old child is still running but the host reports unavailable: %+v", conditions)
	}
	if !conditions.Converged {
		t.Fatalf("a failed install retracted convergence: %+v", conditions)
	}
	if !conditions.Degraded || conditions.ReconcileError == "" {
		t.Fatalf("the failure must still be reported as degraded: %+v", conditions)
	}
}

func TestConditionsReportBinaryReplacementAsProgressing(t *testing.T) {
	m := NewManager(config.Config{BasePort: 5000})
	m.mu.Lock()
	m.conditions = Conditions{Desired: 1, Reconciled: true}
	m.processes["v1"] = &child{status: statusRunning}
	m.downloading["v1"] = struct{}{}
	m.mu.Unlock()

	conditions := m.Conditions()
	if !conditions.Available || conditions.Reconciled ||
		!conditions.Progressing || conditions.Degraded {
		t.Fatalf("unexpected replacement conditions: %+v", conditions)
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

// The coarse readiness answer must use the same live-readiness predicate as the
// per-version one: a running child is not a serving child until its monitor
// vouches for it, and a vouch that has gone stale is no vouch at all.
func TestConditionsServingRequiresALiveReadyChild(t *testing.T) {
	m := NewManager(config.Config{BasePort: 5000})
	c := &child{status: statusRunning}
	m.mu.Lock()
	m.conditions = Conditions{Desired: 1}
	m.processes["v1"] = c
	m.mu.Unlock()

	if got := m.Conditions(); !got.Available || got.Serving {
		t.Fatalf("running child without a fresh vouch: Available=%v Serving=%v",
			got.Available, got.Serving)
	}

	c.serving.Store(true)
	c.servingAt.Store(time.Now().UnixNano())
	if got := m.Conditions(); !got.Serving {
		t.Fatalf("live-ready child not reflected in Serving: %+v", got)
	}

	c.servingAt.Store(time.Now().Add(-2 * childReadyStale).UnixNano())
	if got := m.Conditions(); got.Serving {
		t.Fatal("a stale readiness answer still counts as serving")
	}
}
