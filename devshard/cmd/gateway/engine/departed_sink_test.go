package engine

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"go.uber.org/goleak"
)

type departedRecorder struct {
	recordingSink
	departedMu sync.Mutex
	departed   [][]byte
}

func (r *departedRecorder) WriteDeparted(chunk []byte) {
	r.departedMu.Lock()
	defer r.departedMu.Unlock()
	r.departed = append(r.departed, bytes.Clone(chunk))
}

func (r *departedRecorder) departedBytes() string {
	r.departedMu.Lock()
	defer r.departedMu.Unlock()
	return string(bytes.Join(r.departed, nil))
}

// Test flow:
//  1. Gate a client that takes departed bytes and write one chunk while it is connected.
//  2. Cancel the client and write two more chunks.
//  3. Assert the connected chunk reached the client, both later chunks reached the sink in order, and every write reported success.
func TestGateHandsADepartedClientsBytesToItsSink(t *testing.T) {
	clientCtx, cancel := context.WithCancel(context.Background())
	recorder := &departedRecorder{}
	gated := newDrain(clientCtx, time.Minute).gate(recorder)

	if _, err := gated.Write([]byte("connected;")); err != nil {
		t.Fatalf("write while connected: %v", err)
	}
	cancel()
	for _, chunk := range []string{"first;", "second;"} {
		if written, err := gated.Write([]byte(chunk)); err != nil || written != len(chunk) {
			t.Fatalf("Write(%q) = %d, %v, want %d, nil", chunk, written, err, len(chunk))
		}
	}

	if got := string(recorder.forwarded()); got != "connected;" {
		t.Fatalf("client received %q, want only what it was there for", got)
	}
	if got := recorder.departedBytes(); got != "first;second;" {
		t.Fatalf("sink received %q, want the bytes after departure in order", got)
	}
}

// Test flow:
//  1. Gate a client that takes departed bytes, whose socket write fails.
//  2. Write one chunk, then another.
//  3. Assert the failed chunk departed the client and the next chunk went to the sink.
func TestGateDepartsAClientOnAFailedWriteAndThenFeedsItsSink(t *testing.T) {
	failing := &failingDeparted{}
	contexts := newDrain(context.Background(), time.Minute)
	gated := contexts.gate(failing)

	if _, err := gated.Write([]byte("lost;")); err != nil {
		t.Fatalf("Write() = %v, want the host's write to succeed", err)
	}
	if _, err := gated.Write([]byte("kept;")); err != nil {
		t.Fatalf("Write() = %v, want the host's write to succeed", err)
	}

	if got := failing.departedBytes(); got != "kept;" {
		t.Fatalf("sink received %q, want only the chunk after the failed write", got)
	}
}

type failingDeparted struct{ departedRecorder }

func (f *failingDeparted) Write([]byte) (int, error) { return 0, errors.New("socket closed") }

// Test flow:
//  1. Start a race whose host streams content and then a tail it is paused before sending, over a client that takes departed bytes.
//  2. Disconnect the client mid-stream and let the host finish.
//  3. Assert the client received only the content sent before it left and the sink received the tail.
func TestRunRaceFeedsTheSinkTheWinnersBytesAfterTheClientLeft(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	fixture := newRaceFixture(settledPolicy(), 1)
	fixture.deps.DrainTimeout = time.Hour
	recorder := &departedRecorder{}
	fixture.request.Client = recorder
	streaming := make(chan uint64, 1)
	resume := make(chan struct{})
	fixture.host(200, 0, "host-0", &hostScript{
		receipt:   true,
		chunks:    []string{contentChunk(200), "data: tail\n\n"},
		streaming: streaming,
		resume:    resume,
		confirmed: true,
		finished:  true,
	})

	clientCtx, disconnect := context.WithCancel(context.Background())
	returned := make(chan error, 1)
	go func() {
		_, err := fixture.run(clientCtx)
		returned <- err
	}()
	<-streaming
	disconnect()
	if err := <-returned; !errors.Is(err, context.Canceled) {
		t.Fatalf("runRace error = %v, want context.Canceled", err)
	}
	close(resume)
	<-fixture.reported

	if got := string(recorder.forwarded()); got != contentChunk(200) {
		t.Fatalf("client received %q, want only the content sent before it left", got)
	}
	if got := recorder.departedBytes(); got != "data: tail\n\n" {
		t.Fatalf("sink received %q, want the tail sent after the client left", got)
	}
}

// Test flow:
//  1. Run a completed race with `OnOutcome` and `OnFinished` hooks that record the order they ran in.
//  2. Assert `OnOutcome` ran once, before `OnFinished`, with the race's final outcome.
func TestRunReportsTheOutcomeOnceBeforeFinishing(t *testing.T) {
	sim := newSimulator(t, settledPolicy(), 1, qwenModel)
	sim.clock.step = time.Millisecond
	sim.host(10, 0, "host-0", &hostScript{
		receipt:   true,
		chunks:    []string{contentEvent("hello")},
		confirmed: true,
		finished:  true,
	})
	var order []string
	var seen RaceOutcome
	request := sim.profile()
	request.OnOutcome = func(outcome RaceOutcome) {
		order = append(order, "outcome")
		seen = outcome
	}
	request.OnFinished = func() { order = append(order, "finished") }

	if _, err := sim.engine.Run(context.Background(), request, sim.client); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if want := []string{"outcome", "finished"}; !slices.Equal(order, want) {
		t.Fatalf("hook order = %v, want %v", order, want)
	}
	if !seen.Succeeded || seen.WinnerNonce != 10 {
		t.Fatalf("OnOutcome got succeeded=%v winner=%d, want the final outcome", seen.Succeeded, seen.WinnerNonce)
	}
}

// Test flow:
//  1. Run a race whose client leaves mid-stream, with `OnOutcome` and `OnFinished` hooks.
//  2. Assert neither hook ran when `Run` returned `context.Canceled`.
//  3. Let the host finish, then assert `OnOutcome` ran once with a succeeded outcome, before `OnFinished`.
func TestRunReportsTheOutcomeOfARaceThatOutlivedItsClient(t *testing.T) {
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
	var mu sync.Mutex
	var order []string
	var succeeded bool
	note := func(step string) {
		mu.Lock()
		defer mu.Unlock()
		order = append(order, step)
	}
	finished := make(chan struct{})
	request := sim.profile()
	request.OnOutcome = func(outcome RaceOutcome) {
		note("outcome")
		mu.Lock()
		succeeded = outcome.Succeeded
		mu.Unlock()
	}
	request.OnFinished = func() {
		note("finished")
		close(finished)
	}

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
	mu.Lock()
	early := len(order)
	mu.Unlock()
	if early != 0 {
		t.Fatalf("hooks ran %d times while the host still streams, want 0", early)
	}

	close(resume)
	waitForValue(t, finished, "OnFinished after the race reported")
	sim.settleAll()

	mu.Lock()
	defer mu.Unlock()
	if want := []string{"outcome", "finished"}; !slices.Equal(order, want) {
		t.Fatalf("hook order = %v, want %v", order, want)
	}
	if !succeeded {
		t.Fatal("OnOutcome got a failed outcome, want the race the host completed")
	}
}
