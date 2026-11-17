package engine

import (
	"context"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"devshard/cmd/gateway/scheduler"
)

const attemptEndWaitLimit = 5 * time.Second

// sendWatch reports when a host's send has returned.
type sendWatch struct {
	*scriptedTarget
	returned chan uint64
}

func (w sendWatch) Send(ctx context.Context, nonce scheduler.Prepared, stream io.Writer, onReceipt func()) (Response, error) {
	defer func() { w.returned <- nonce.Nonce() }()
	return w.scriptedTarget.Send(ctx, nonce, stream, onReceipt)
}

func (w sendWatch) Target(string) (DispatchTarget, bool) { return w, true }

// breakingTimer panics on the coordinator's next re-arm once broken; nothing but the coordinator arms it.
type breakingTimer struct {
	raceTimer
	broken *atomic.Bool
}

func (t breakingTimer) Reset(delay time.Duration) {
	if t.broken.Load() {
		panic("timer exploded")
	}
	t.raceTimer.Reset(delay)
}

// Test flow:
//  1. Start a race whose host receipts and then holds its stream open until its attempt is cancelled, with a timer that panics on its next re-arm once the host has arrived.
//  2. Release the host so its receipt wakes the coordinator, which panics re-arming its timer.
//  3. Assert the panic reaches the caller and the host's send still returns: a panicking coordinator cancels the attempts it launched instead of leaving them running for nobody.
func TestAPanickingCoordinatorCancelsTheAttemptsItLaunched(t *testing.T) {
	fixture := newRaceFixture(settledPolicy(), 1)
	arrive, release := make(chan uint64, 1), make(chan struct{})
	fixture.host(90, 0, "host-0", &hostScript{arrive: arrive, release: release, receipt: true, hold: true})
	watch := sendWatch{scriptedTarget: fixture.target, returned: make(chan uint64, 1)}
	fixture.deps.Targets = watch
	var timerBroken atomic.Bool
	fixture.deps.Timer = func() raceTimer { return breakingTimer{raceTimer: fixture.clock, broken: &timerBroken} }

	recovered := make(chan any, 1)
	go func() {
		defer func() { recovered <- recover() }()
		_, _ = fixture.run(context.Background())
	}()
	<-arrive
	timerBroken.Store(true)
	close(release)

	if panicked := <-recovered; panicked == nil {
		t.Fatal("the coordinator's panic was swallowed")
	}
	select {
	case <-watch.returned:
	case <-time.After(attemptEndWaitLimit):
		t.Fatal("the host's send never returned: the panicking coordinator left its attempt running")
	}
}

type committedPicker struct {
	*stubPicker
	entered    chan struct{}
	release    chan struct{}
	assignment scheduler.Assignment
}

func (p committedPicker) Pick(context.Context, scheduler.RequestProfile) (scheduler.Assignment, error) {
	p.entered <- struct{}{}
	<-p.release
	return p.assignment, nil
}

// Test flow:
//  1. Start a speculative pick on a paused coordinator whose picker hands over a committed nonce only once released.
//  2. Abandon the coordinator the way a panic does, then release the pick.
//  3. Assert the assignment's host slot and escrow hold are both given back, though no coordinator reads the pick any more.
func TestAPanickingCoordinatorGivesBackAPickAnsweredAfterIt(t *testing.T) {
	fixture := newRaceFixture(settledPolicy(), 2)
	slotReleased, holdReleased := make(chan struct{}, 1), make(chan struct{}, 1)
	picker := committedPicker{
		stubPicker: fixture.picker,
		entered:    make(chan struct{}, 1),
		release:    make(chan struct{}),
		assignment: scheduler.Assignment{
			Escrow:     "escrow-1",
			Host:       "host-1",
			Nonce:      fakePrepared{nonce: 91, hostIdx: 1},
			HostSlot:   func() { slotReleased <- struct{}{} },
			EscrowHold: func() { holdReleased <- struct{}{} },
		},
	}
	fixture.deps.Picker = picker
	coordinator := pausedCoordinator(fixture, 2)

	coordinator.startPick(coordinator.decision, coordinator.request.Params)
	waitForValue(t, picker.entered, "the pick reaching the scheduler")
	coordinator.abandon()
	close(picker.release)

	waitForValue(t, slotReleased, "the abandoned pick's host slot being given back")
	waitForValue(t, holdReleased, "the abandoned pick's escrow hold being given back")
}
