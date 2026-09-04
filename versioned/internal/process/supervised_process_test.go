package process

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"versioned/internal/config"
	"versioned/internal/oracle"
)

const supervisedProcessHelperEnv = "VERSIOND_SUPERVISED_PROCESS_HELPER"

func TestSupervisedProcessHelper(t *testing.T) {
	if os.Getenv(supervisedProcessHelperEnv) == "" {
		return
	}

	var term chan os.Signal
	switch os.Getenv(supervisedProcessHelperEnv) {
	case "graceful":
		term = make(chan os.Signal, 1)
		signal.Notify(term, syscall.SIGTERM)
	case "ignore-term":
		signal.Ignore(syscall.SIGTERM)
	case "exit":
	default:
		os.Exit(2)
	}

	readyFile := os.Getenv("VERSIOND_SUPERVISED_PROCESS_READY_FILE")
	if err := os.WriteFile(readyFile, []byte("ready"), 0o600); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if os.Getenv(supervisedProcessHelperEnv) == "exit" {
		return
	}
	if term != nil {
		<-term
		return
	}
	select {}
}

func TestProcessTransitionTable(t *testing.T) {
	states := []processState{
		processStateRunning,
		processStateTerminating,
		processStateKilling,
		processStateExited,
	}
	events := []processEvent{
		processEventStopRequested,
		processEventForceRequested,
		processEventGraceExpired,
		processEventExited,
	}
	expected := map[processTransitionKey]processTransitionSpec{
		{state: processStateRunning, event: processEventStopRequested}: {
			next:   processStateTerminating,
			action: processActionSignalTerm,
		},
		{state: processStateRunning, event: processEventForceRequested}: {
			next:   processStateKilling,
			action: processActionSignalKill,
		},
		{state: processStateRunning, event: processEventExited}: {
			next:   processStateExited,
			action: processActionFinish,
		},
		{state: processStateTerminating, event: processEventForceRequested}: {
			next:   processStateKilling,
			action: processActionSignalKill,
		},
		{state: processStateTerminating, event: processEventGraceExpired}: {
			next:   processStateKilling,
			action: processActionSignalKill,
		},
		{state: processStateTerminating, event: processEventExited}: {
			next:   processStateExited,
			action: processActionFinish,
		},
		{state: processStateKilling, event: processEventExited}: {
			next:   processStateExited,
			action: processActionFinish,
		},
	}

	for _, state := range states {
		for _, event := range events {
			key := processTransitionKey{state: state, event: event}
			got, gotOK := processTransitionTable[key]
			want, wantOK := expected[key]
			if gotOK != wantOK || got != want {
				t.Errorf(
					"transition for %s + %s = %+v, %t; want %+v, %t",
					state,
					event,
					got,
					gotOK,
					want,
					wantOK,
				)
			}
		}
	}
}

func TestProcessTransitionTableActionsMatchTargets(t *testing.T) {
	knownStates := map[processState]bool{
		processStateRunning:     true,
		processStateTerminating: true,
		processStateKilling:     true,
		processStateExited:      true,
	}
	knownEvents := map[processEvent]bool{
		processEventStopRequested:  true,
		processEventForceRequested: true,
		processEventGraceExpired:   true,
		processEventExited:         true,
	}
	exitEdges := make(map[processState]bool)
	for key, transition := range processTransitionTable {
		if !knownStates[key.state] {
			t.Errorf("transition has unknown source state %s", key.state)
		}
		if !knownEvents[key.event] {
			t.Errorf("transition has unknown event %s", key.event)
		}
		if !knownStates[transition.next] {
			t.Errorf("%s + %s targets unknown state %s", key.state, key.event, transition.next)
		}
		switch transition.action {
		case processActionSignalTerm:
			if transition.next != processStateTerminating {
				t.Errorf("SIGTERM action targets %s", transition.next)
			}
		case processActionSignalKill:
			if transition.next != processStateKilling {
				t.Errorf("SIGKILL action targets %s", transition.next)
			}
		case processActionFinish:
			if key.event != processEventExited ||
				transition.next != processStateExited {
				t.Errorf(
					"finish action maps %s + %s to %s",
					key.state,
					key.event,
					transition.next,
				)
			}
			exitEdges[key.state] = true
		default:
			t.Errorf("%s + %s has unknown action %d", key.state, key.event, transition.action)
		}
	}
	for _, state := range []processState{
		processStateRunning,
		processStateTerminating,
		processStateKilling,
	} {
		if !exitEdges[state] {
			t.Errorf("%s has no process-exited edge", state)
		}
	}
}

func TestSupervisedProcessNaturalExitReaps(t *testing.T) {
	stop := make(chan struct{})
	force := make(chan struct{})
	proc := startSupervisedTestProcess(t, "exit", time.Second, stop, force)

	if err := waitForSupervisedProcess(proc, 5*time.Second); err != nil {
		t.Fatalf("natural process exit: %v", err)
	}
	if proc.Escalated() {
		t.Fatal("naturally exited process recorded escalation")
	}
	if got := proc.State(); got != processStateExited {
		t.Fatalf("process state = %s, want exited", got)
	}
	assertProcessReaped(t, proc)
}

func TestSupervisedProcessForceStopsRunningProcessAndReaps(t *testing.T) {
	stop := make(chan struct{})
	force := make(chan struct{})
	proc := startSupervisedTestProcess(t, "ignore-term", time.Hour, stop, force)

	proc.ForceStop()
	if err := waitForSupervisedProcess(proc, time.Second); err == nil {
		t.Fatal("SIGKILLed process returned a nil wait error")
	}
	if !proc.Escalated() {
		t.Fatal("forced process did not record SIGKILL escalation")
	}
	if got := proc.State(); got != processStateExited {
		t.Fatalf("process state = %s, want exited", got)
	}
	assertProcessReaped(t, proc)
}

func TestSupervisedProcessStopsGracefullyAndReaps(t *testing.T) {
	stop := make(chan struct{})
	force := make(chan struct{})
	// The race runtime delays helper-process exit while flushing its report.
	proc := startSupervisedTestProcess(t, "graceful", 3*time.Second, stop, force)

	close(stop)
	if err := waitForSupervisedProcess(proc, 5*time.Second); err != nil {
		t.Fatalf("graceful process wait: %v", err)
	}
	if proc.Escalated() {
		t.Fatal("graceful process unexpectedly escalated to SIGKILL")
	}
	if got := proc.State(); got != processStateExited {
		t.Fatalf("process state = %s, want exited", got)
	}
	assertProcessReaped(t, proc)
}

func TestSupervisedProcessEscalatesAfterGraceAndReaps(t *testing.T) {
	const grace = 100 * time.Millisecond
	stop := make(chan struct{})
	force := make(chan struct{})
	proc := startSupervisedTestProcess(t, "ignore-term", grace, stop, force)

	started := time.Now()
	close(stop)
	if err := waitForSupervisedProcess(proc, 2*time.Second); err == nil {
		t.Fatal("SIGKILLed process returned a nil wait error")
	}
	if elapsed := time.Since(started); elapsed < grace/2 {
		t.Fatalf("process escalated after %s, want graceful wait first", elapsed)
	}
	if !proc.Escalated() {
		t.Fatal("process did not record SIGKILL escalation")
	}
	if got := proc.State(); got != processStateExited {
		t.Fatalf("process state = %s, want exited", got)
	}
	assertProcessReaped(t, proc)
}

func TestManagerShutdownDeadlineForcesAndReapsChildren(t *testing.T) {
	childCtx, childStop := context.WithCancel(context.Background())
	force := make(chan struct{})
	proc := startSupervisedTestProcess(t, "ignore-term", time.Hour, childCtx.Done(), force)
	done := make(chan struct{})
	go func() {
		_ = proc.Wait()
		close(done)
	}()

	c := &child{
		version:     oracle.Version{Name: "v1"},
		stop:        childStop,
		forceStopCh: force,
		done:        done,
		status:      statusRunning,
	}
	m := NewManager(config.Config{BasePort: 5000})
	m.mu.Lock()
	m.processes[c.version.Name] = c
	m.mu.Unlock()

	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancelShutdown()
	err := m.Shutdown(shutdownCtx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Shutdown error = %v, want context deadline exceeded", err)
	}
	if !proc.Escalated() {
		t.Fatal("manager deadline did not escalate child to SIGKILL")
	}
	select {
	case <-c.Done():
	default:
		t.Fatal("Shutdown returned before the child was reaped")
	}
	assertProcessReaped(t, proc)
}

func startSupervisedTestProcess(
	t *testing.T,
	mode string,
	grace time.Duration,
	stop <-chan struct{},
	force <-chan struct{},
) *supervisedProcess {
	t.Helper()
	readyFile := filepath.Join(t.TempDir(), "ready")
	cmd := exec.Command(os.Args[0], "-test.run=^TestSupervisedProcessHelper$")
	cmd.Env = append(os.Environ(),
		supervisedProcessHelperEnv+"="+mode,
		"VERSIOND_SUPERVISED_PROCESS_READY_FILE="+readyFile,
	)
	proc, err := startSupervisedProcess(cmd, stop, force, grace)
	if err != nil {
		t.Fatalf("start helper process: %v", err)
	}
	t.Cleanup(func() {
		proc.ForceStop()
		_ = proc.Wait()
	})

	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(readyFile); err == nil {
			return proc
		}
		select {
		case <-proc.Done():
			t.Fatalf("helper process exited before readiness: %v", proc.Wait())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("helper process did not report readiness")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func waitForSupervisedProcess(proc *supervisedProcess, timeout time.Duration) error {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-proc.Done():
		return proc.Wait()
	case <-timer.C:
		return fmt.Errorf("process did not exit within %s", timeout)
	}
}

func assertProcessReaped(t *testing.T, proc *supervisedProcess) {
	t.Helper()
	if err := proc.cmd.Process.Signal(syscall.Signal(0)); !errors.Is(err, os.ErrProcessDone) {
		t.Fatalf("signal reaped process error = %v, want os.ErrProcessDone", err)
	}
}
