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
	default:
		os.Exit(2)
	}

	readyFile := os.Getenv("VERSIOND_SUPERVISED_PROCESS_READY_FILE")
	if err := os.WriteFile(readyFile, []byte("ready"), 0o600); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if term != nil {
		<-term
		return
	}
	select {}
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
