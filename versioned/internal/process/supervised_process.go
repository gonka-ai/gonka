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

type processEvent uint8

const (
	processEventStopRequested processEvent = iota
	processEventForceRequested
	processEventGraceExpired
	processEventExited
)

type processAction uint8

const (
	processActionSignalTerm processAction = iota
	processActionSignalKill
	processActionFinish
)

type processTransitionKey struct {
	state processState
	event processEvent
}

type processTransitionSpec struct {
	next   processState
	action processAction
}

var processTransitionTable = map[processTransitionKey]processTransitionSpec{
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

func (e processEvent) String() string {
	switch e {
	case processEventStopRequested:
		return "stop_requested"
	case processEventForceRequested:
		return "force_requested"
	case processEventGraceExpired:
		return "grace_expired"
	case processEventExited:
		return "exited"
	default:
		return fmt.Sprintf("processEvent(%d)", e)
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

	stopCh := p.stop
	forceCh := (<-chan struct{})(p.force)
	externalForceCh := p.externalForce
	var graceTimer *time.Timer
	var graceCh <-chan time.Time
	stopGraceTimer := func() {
		if graceTimer != nil {
			graceTimer.Stop()
			graceTimer = nil
			graceCh = nil
		}
	}
	defer stopGraceTimer()

	for {
		var event processEvent
		var waitErr error
		select {
		case waitErr = <-waitCh:
			event = processEventExited
		case <-stopCh:
			stopCh = nil
			event = processEventStopRequested
		case <-forceCh:
			forceCh = nil
			event = processEventForceRequested
		case <-externalForceCh:
			externalForceCh = nil
			event = processEventForceRequested
		case <-graceCh:
			graceCh = nil
			event = processEventGraceExpired
		}

		transition, ok := p.applyEvent(event)
		if !ok {
			continue
		}
		switch transition.action {
		case processActionSignalTerm:
			p.signal(syscall.SIGTERM)
			graceTimer = time.NewTimer(p.grace)
			graceCh = graceTimer.C
		case processActionSignalKill:
			stopGraceTimer()
			stopCh = nil
			forceCh = nil
			externalForceCh = nil
			p.signal(syscall.SIGKILL)
		case processActionFinish:
			stopGraceTimer()
			p.finish(waitErr)
			return
		}
	}
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

func (p *supervisedProcess) applyEvent(
	event processEvent,
) (processTransitionSpec, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	key := processTransitionKey{state: p.state, event: event}
	transition, ok := processTransitionTable[key]
	if !ok {
		slog.Error(
			"invalid child process event",
			"state", p.state.String(),
			"event", event.String(),
			"pid", p.cmd.Process.Pid,
		)
		return processTransitionSpec{}, false
	}
	p.state = transition.next
	if transition.action == processActionSignalKill {
		p.escalated = true
	}
	return transition, true
}

func (p *supervisedProcess) finish(err error) {
	p.mu.Lock()
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
