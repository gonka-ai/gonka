package engine

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/goleak"
)

func countedFinish() (func(), *atomic.Int64, chan struct{}) {
	var calls atomic.Int64
	called := make(chan struct{}, 1)
	return func() {
		calls.Add(1)
		select {
		case called <- struct{}{}:
		default:
		}
	}, &calls, called
}

// Test flow:
//  1. Start a one-host simulator whose host streams content, then pauses before its tail chunk.
//  2. Run the race with a counting `OnFinished` hook and disconnect the client once the host streams.
//  3. Assert `Run` returns `context.Canceled` and the hook has not run while the host still streams.
//  4. Resume the host, wait for the race's report, and assert the hook ran exactly once.
func TestRunFinishesOnlyOnceTheDrainedRaceReports(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	sim := newSimulator(t, settledPolicy(), 1, qwenModel)
	sim.clock.step = time.Millisecond
	streaming := make(chan uint64, 1)
	resume := make(chan struct{})
	sim.host(10, 0, "host-0", &hostScript{
		receipt:   true,
		chunks:    []string{contentEvent("hello"), contentEvent("tail")},
		streaming: streaming,
		resume:    resume,
		confirmed: true,
		finished:  true,
	})
	finish, calls, called := countedFinish()
	request := sim.profile()
	request.OnFinished = finish

	clientCtx, disconnect := context.WithCancel(context.Background())
	returned := make(chan error, 1)
	go func() {
		_, err := sim.engine.Run(clientCtx, request, sim.client)
		returned <- err
	}()
	<-streaming
	disconnect()
	if err := <-returned; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want context.Canceled", err)
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("OnFinished ran %d times while the host still streams, want 0", got)
	}

	close(resume)
	sim.reported(t)
	select {
	case <-called:
	case <-time.After(10 * time.Second):
		t.Fatal("OnFinished never ran after the race reported")
	}
	sim.settleAll()
	if got := calls.Load(); got != 1 {
		t.Fatalf("OnFinished ran %d times, want 1", got)
	}
}

// Test flow:
//  1. Start a one-host simulator whose host answers at once.
//  2. Run the race to completion with a counting `OnFinished` hook.
//  3. Assert the hook ran exactly once by the time `Run` returned.
func TestRunFinishesACompletedRaceBeforeReturning(t *testing.T) {
	sim := newSimulator(t, settledPolicy(), 1, qwenModel)
	sim.clock.step = time.Millisecond
	sim.host(10, 0, "host-0", &hostScript{
		receipt:   true,
		chunks:    []string{contentEvent("hello")},
		confirmed: true,
		finished:  true,
	})
	finish, calls, _ := countedFinish()
	request := sim.profile()
	request.OnFinished = finish

	if _, err := sim.engine.Run(context.Background(), request, sim.client); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("OnFinished ran %d times, want 1", got)
	}
}

// Test flow:
//  1. Stop a simulator's engine.
//  2. Run a request with a counting `OnFinished` hook.
//  3. Assert `Run` returns `ErrStopped` and the hook ran exactly once.
func TestRunFinishesARequestAStoppedEngineRefuses(t *testing.T) {
	sim := newSimulator(t, settledPolicy(), 1, qwenModel)
	sim.engine.Stop()
	finish, calls, _ := countedFinish()
	request := sim.profile()
	request.OnFinished = finish

	if _, err := sim.engine.Run(context.Background(), request, sim.client); !errors.Is(err, ErrStopped) {
		t.Fatalf("Run() error = %v, want ErrStopped", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("OnFinished ran %d times, want 1", got)
	}
}

// Test flow:
//  1. Start a one-host simulator and a request whose `OnEscrow` panics once the race has picked its escrow.
//  2. Run the race with a counting `OnFinished` hook and recover the panic.
//  3. Assert the panic reached the caller and the hook ran exactly once.
func TestRunFinishesARaceThatPanics(t *testing.T) {
	sim := newSimulator(t, settledPolicy(), 1, qwenModel)
	sim.host(10, 0, "host-0", &hostScript{receipt: true, chunks: []string{contentEvent("hello")}, confirmed: true, finished: true})
	finish, calls, _ := countedFinish()
	request := sim.profile()
	request.OnFinished = finish
	request.OnEscrow = func(string) { panic("escrow header") }

	panicked := func() (recovered any) {
		defer func() { recovered = recover() }()
		_, _ = sim.engine.Run(context.Background(), request, sim.client)
		return nil
	}()
	if panicked == nil {
		t.Fatal("Run() returned, want the panic to reach the caller")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("OnFinished ran %d times, want 1", got)
	}
}
