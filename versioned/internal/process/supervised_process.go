package process

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

type processState uint8

const (
	processStateRunning processState = iota
	processStateTerminating
	processStateKilling
	processStateExited
)

func (s processState) String() string {
	switch s {
	case processStateRunning:
		return "running"
	case processStateTerminating:
		return "terminating"
	case processStateKilling:
		return "killing"
	case processStateExited:
		return "exited"
	default:
		return fmt.Sprintf("processState(%d)", s)
	}
}

// supervisedProcess owns signal delivery and reaping for one process
// incarnation. The graceful timeout controls escalation to SIGKILL; completion
// is always confirmed by cmd.Wait before Done is closed.
type supervisedProcess struct {
	cmd           *exec.Cmd
	grace         time.Duration
	stop          <-chan struct{}
	externalForce <-chan struct{}

	force     chan struct{}
	forceOnce sync.Once
	done      chan struct{}

	mu        sync.Mutex
	state     processState
	err       error
	escalated bool
}

func startSupervisedProcess(
	cmd *exec.Cmd,
	stop <-chan struct{},
	externalForce <-chan struct{},
	grace time.Duration,
) (*supervisedProcess, error) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	p := &supervisedProcess{
		cmd:           cmd,
		grace:         grace,
		stop:          stop,
		externalForce: externalForce,
		force:         make(chan struct{}),
		done:          make(chan struct{}),
		state:         processStateRunning,
	}
	go p.run()
	return p, nil
}

func (p *supervisedProcess) run() {
	waitCh := make(chan error, 1)
	go func() {
		waitCh <- p.cmd.Wait()
	}()

	var err error
	select {
	case err = <-waitCh:
		p.complete(err)
		return
	case <-p.stop:
		p.transition(processStateTerminating)
		p.signal(syscall.SIGTERM)
	case <-p.force:
		err = p.killAndWait(waitCh)
		p.complete(err)
		return
	case <-p.externalForce:
		err = p.killAndWait(waitCh)
		p.complete(err)
		return
	}

	timer := time.NewTimer(p.grace)
	defer timer.Stop()
	select {
	case err = <-waitCh:
	case <-timer.C:
		err = p.killAndWait(waitCh)
	case <-p.force:
		err = p.killAndWait(waitCh)
	case <-p.externalForce:
		err = p.killAndWait(waitCh)
	}
	p.complete(err)
}

func (p *supervisedProcess) killAndWait(waitCh <-chan error) error {
	p.transition(processStateKilling)
	p.mu.Lock()
	p.escalated = true
	p.mu.Unlock()
	p.signal(syscall.SIGKILL)
	return <-waitCh
}

func (p *supervisedProcess) signal(signal syscall.Signal) {
	if err := signalProcessGroup(p.cmd.Process, signal); err != nil {
		slog.Warn(
			"child process signal failed",
			"pid", p.cmd.Process.Pid,
			"signal", signal.String(),
			"error", err,
		)
	}
}

func signalProcessGroup(process *os.Process, signal syscall.Signal) error {
	if process == nil {
		return errors.New("child process has not started")
	}
	if err := syscall.Kill(-process.Pid, signal); err != nil {
		if errors.Is(err, syscall.ESRCH) || errors.Is(err, os.ErrProcessDone) {
			return nil
		}
		fallbackErr := process.Signal(signal)
		if fallbackErr == nil || errors.Is(fallbackErr, os.ErrProcessDone) {
			return nil
		}
		return errors.Join(err, fallbackErr)
	}
	return nil
}

func (p *supervisedProcess) transition(next processState) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !validProcessTransition(p.state, next) {
		slog.Error(
			"invalid child process state transition",
			"from", p.state.String(),
			"to", next.String(),
			"pid", p.cmd.Process.Pid,
		)
		return
	}
	p.state = next
}

func validProcessTransition(from, to processState) bool {
	switch from {
	case processStateRunning:
		return to == processStateTerminating || to == processStateKilling || to == processStateExited
	case processStateTerminating:
		return to == processStateKilling || to == processStateExited
	case processStateKilling:
		return to == processStateExited
	case processStateExited:
		return false
	default:
		return false
	}
}

func (p *supervisedProcess) complete(err error) {
	p.mu.Lock()
	if validProcessTransition(p.state, processStateExited) {
		p.state = processStateExited
	} else {
		slog.Error(
			"invalid child process completion state",
			"state", p.state.String(),
			"pid", p.cmd.Process.Pid,
		)
	}
	p.err = err
	p.mu.Unlock()
	close(p.done)
}

func (p *supervisedProcess) ForceStop() {
	p.forceOnce.Do(func() {
		close(p.force)
	})
}

func (p *supervisedProcess) Done() <-chan struct{} {
	return p.done
}

func (p *supervisedProcess) Wait() error {
	<-p.done
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.err
}

func (p *supervisedProcess) State() processState {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.state
}

func (p *supervisedProcess) Escalated() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.escalated
}
