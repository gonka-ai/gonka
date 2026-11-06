package engine

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"go.uber.org/goleak"

	"devshard/cmd/gateway/config"
)

type traceKey struct{}

type flushRecorder struct {
	written  bytes.Buffer
	flushes  int
	writeErr error
}

func (r *flushRecorder) Write(chunk []byte) (int, error) {
	if r.writeErr != nil {
		return 0, r.writeErr
	}
	return r.written.Write(chunk)
}

func (r *flushRecorder) Flush() { r.flushes++ }

// Test flow:
//  1. Compute `DrainTimeoutFromConfig` from a config with `DrainTimeoutSeconds` set to 2,400.
//  2. Assert it converts to 40 minutes.
//  3. Assert `newDrain` with a zero timeout falls back to `defaultDrainTimeout`.
func TestDrainTimeoutFromConfig(t *testing.T) {
	timeout := DrainTimeoutFromConfig(config.Stream{DrainTimeoutSeconds: 2_400})
	if timeout != 40*time.Minute {
		t.Fatalf("timeout = %v, want 40m", timeout)
	}
	if unset := newDrain(context.Background(), 0); unset.timeout != defaultDrainTimeout {
		t.Fatalf("unset timeout = %v, want %v", unset.timeout, defaultDrainTimeout)
	}
}

// Test flow:
//  1. Build a `newDrain` over a cancellable client context carrying a trace value.
//  2. Cancel the client context.
//  3. Assert the drain's `clientGone()` signal fires while its race context stays uncancelled and keeps the client's trace value.
//  4. Assert `clientErr()` reports the client's cancellation.
func TestDrainRaceContextOutlivesTheClientKeepingItsValues(t *testing.T) {
	clientCtx, cancel := context.WithCancel(context.WithValue(context.Background(), traceKey{}, "request-1"))
	contexts := newDrain(clientCtx, time.Minute)
	cancel()

	select {
	case <-contexts.clientGone():
	default:
		t.Fatal("client cancellation is not observable")
	}
	if err := contexts.race.Err(); err != nil {
		t.Fatalf("race context error = %v, want the race to outlive the client", err)
	}
	if trace := contexts.race.Value(traceKey{}); trace != "request-1" {
		t.Fatalf("race context value = %v, want the client's", trace)
	}
	if !errors.Is(contexts.clientErr(), context.Canceled) {
		t.Fatalf("client error = %v, want context.Canceled", contexts.clientErr())
	}
}

// Test flow:
//  1. Build a `newDrain` with a one-minute timeout.
//  2. Compute its deadline for a zero client-done time and assert none is armed.
//  3. Compute its deadline for a real client-done time and assert it lands one minute later.
func TestDrainDeadlineArmsOnlyOnClientDone(t *testing.T) {
	contexts := newDrain(context.Background(), time.Minute)
	if armed := contexts.deadline(time.Time{}); !armed.IsZero() {
		t.Fatalf("deadline while the client is connected = %v, want none", armed)
	}
	if armed := contexts.deadline(testEpoch); !armed.Equal(testEpoch.Add(time.Minute)) {
		t.Fatalf("deadline = %v, want %v", armed, testEpoch.Add(time.Minute))
	}
}

// Test flow:
//  1. Build a `newDrain` gate over a `flushRecorder` while the client is still connected.
//  2. Write and flush while connected; assert the bytes and flush reached the recorder.
//  3. Cancel the client context, then write again.
//  4. Assert the write still succeeds (the host's send is not blocked) but the bytes never reach the recorder, and that flushes already made were not repeated.
//  5. Assert gating a nil client returns no gate at all.
func TestClientStreamDropsBytesOnceTheClientIsGoneAndKeepsFlushing(t *testing.T) {
	clientCtx, cancel := context.WithCancel(context.Background())
	recorder := &flushRecorder{}
	gated := newDrain(clientCtx, time.Minute).gate(recorder)

	if _, err := gated.Write([]byte("connected")); err != nil {
		t.Fatalf("write while connected: %v", err)
	}
	gated.(interface{ Flush() }).Flush()
	cancel()

	written, err := gated.Write([]byte("departed"))
	if err != nil {
		t.Fatalf("write after departure = %v, want the host's write to succeed", err)
	}
	if written != len("departed") {
		t.Fatalf("written = %d, want %d", written, len("departed"))
	}
	if got := recorder.written.String(); got != "connected" {
		t.Fatalf("client received %q, want only what it was still there for", got)
	}
	if recorder.flushes != 1 {
		t.Fatalf("flushes = %d, want the gate to pass Flush through", recorder.flushes)
	}
	if gate := newDrain(clientCtx, time.Minute).gate(nil); gate != nil {
		t.Fatalf("gate over no client = %v, want none", gate)
	}
}

// Test flow:
//  1. Build a `newDrain` gate over a `flushRecorder` configured to fail on write, while the client is still connected.
//  2. Write through the gate.
//  3. Assert the recorder's failure propagates unchanged.
func TestClientStreamPropagatesAConnectedClientsWriteError(t *testing.T) {
	failure := errors.New("client write failed")
	recorder := &flushRecorder{writeErr: failure}
	gated := newDrain(context.Background(), time.Minute).gate(recorder)

	if _, err := gated.Write([]byte("chunk")); !errors.Is(err, failure) {
		t.Fatalf("error = %v, want %v", err, failure)
	}
}

// Test flow:
//  1. Start a race with one host that streams content then a tail chunk it is paused before sending.
//  2. Run the race in the background and disconnect the client mid-stream.
//  3. Assert `run` returns `context.Canceled` to the caller.
//  4. Resume the host and assert exactly one outcome is reported, with the winner nonce confirmed, finished, and marked `TerminalWon`.
//  5. Assert the client received only the content sent before it left, not the tail sent after.
//  6. Assert the host slot and perf bracket were released exactly once.
func TestRunRaceCompletesTheWinnerAfterTheClientDisconnects(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	fixture := newRaceFixture(settledPolicy(), 1)
	fixture.deps.DrainTimeout = time.Hour
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

	reported := <-fixture.reported
	if len(fixture.reported) != 0 {
		t.Fatalf("outcomes reported = %d, want 1", 1+len(fixture.reported))
	}
	if len(reported.Attempts) != 1 {
		t.Fatalf("attempts = %d, want 1", len(reported.Attempts))
	}
	winner := reported.Attempts[0]
	if reported.WinnerNonce != 200 || winner.Terminal != TerminalWon || !reported.Succeeded {
		t.Fatalf("winner %d terminal %v succeeded %v, want 200/won/true", reported.WinnerNonce, winner.Terminal, reported.Succeeded)
	}
	if !winner.NonceFinished || !winner.Confirmed {
		t.Fatalf("nonce finished = %v confirmed = %v, want both", winner.NonceFinished, winner.Confirmed)
	}
	forwarded := string(fixture.client.forwarded())
	if !strings.Contains(forwarded, contentChunk(200)) {
		t.Fatalf("client received %q, want what it was still there for", forwarded)
	}
	if strings.Contains(forwarded, "tail") {
		t.Fatalf("client received %q after leaving", forwarded)
	}
	assertOneSlotPerAttempt(t, fixture, len(reported.Attempts))
}

// Test flow:
//  1. Start a race with one host streaming a role chunk then a content chunk that will claim the crown, paused before the claim resolves.
//  2. Run the race in the background and disconnect the client before the claim is answered.
//  3. Assert `run` returns `context.Canceled`.
//  4. Resume the host and assert the crown claim still wins, with the client receiving nothing sent after it left.
//  5. Assert the host slot and perf bracket were released exactly once.
func TestRunRaceAnswersACrownClaimRaisedAfterTheClientLeft(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	fixture := newRaceFixture(settledPolicy(), 1)
	fixture.deps.DrainTimeout = time.Hour
	streaming := make(chan uint64, 1)
	resume := make(chan struct{})
	fixture.host(210, 0, "host-0", &hostScript{
		receipt:   true,
		chunks:    []string{"data: role\n\n", contentChunk(210)},
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

	reported := <-fixture.reported
	if len(fixture.reported) != 0 {
		t.Fatalf("outcomes reported = %d, want 1", 1+len(fixture.reported))
	}
	if reported.WinnerNonce != 210 || reported.Attempts[0].Terminal != TerminalWon {
		t.Fatalf("winner %d terminal %v, want 210/won: the claim went unanswered", reported.WinnerNonce, reported.Attempts[0].Terminal)
	}
	if forwarded := fixture.client.forwarded(); len(forwarded) != 0 {
		t.Fatalf("client received %q after leaving", forwarded)
	}
	assertOneSlotPerAttempt(t, fixture, len(reported.Attempts))
}

// Test flow:
//  1. Start a race with a one-millisecond drain timeout and a host streaming content whose `resume` channel is never closed, so only the drain deadline can end the attempt.
//  2. Run the race in the background and disconnect the client while it streams.
//  3. Assert `run` returns `context.Canceled`.
//  4. Assert the reported outcome names the winner nonce and marks its terminal as `TerminalClientCancelled`.
//  5. Assert the host slot and perf bracket were released exactly once.
func TestRunRaceDrainTimeoutBoundsAHostStreamingPastTheClient(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	fixture := newRaceFixture(settledPolicy(), 1)
	fixture.deps.DrainTimeout = time.Millisecond
	fixture.clock.step = time.Second
	streaming := make(chan uint64, 1)
	fixture.host(220, 0, "host-0", &hostScript{
		receipt:   true,
		chunks:    []string{contentChunk(220)},
		streaming: streaming,
		resume:    make(chan struct{}),
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

	reported := <-fixture.reported
	if len(fixture.reported) != 0 {
		t.Fatalf("outcomes reported = %d, want 1", 1+len(fixture.reported))
	}
	if reported.WinnerNonce != 220 {
		t.Fatalf("winner nonce = %d, want 220", reported.WinnerNonce)
	}
	if terminal := reported.Attempts[0].Terminal; terminal != TerminalClientCancelled {
		t.Fatalf("terminal = %v, want client cancelled", terminal)
	}
	assertOneSlotPerAttempt(t, fixture, len(reported.Attempts))
}

func assertOneSlotPerAttempt(t *testing.T, fixture *raceFixture, attempts int) {
	t.Helper()
	for range attempts {
		select {
		case <-fixture.limiter.releases:
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for %d host-slot releases", attempts)
		}
	}
	if extra := len(fixture.limiter.releases); extra != 0 {
		t.Fatalf("host-slot releases = %d more than attempts, want one each", extra)
	}
	if acquired, released := fixture.perf.counts(); acquired != attempts || released != attempts {
		t.Fatalf("perf bracket = %d/%d, want %d/%d", acquired, released, attempts, attempts)
	}
}

type countingFlusher struct {
	bytes.Buffer
	flushes int
}

func (f *countingFlusher) Flush() { f.flushes++ }

// Test flow:
//  1. Gate a flushing writer on a client that has already left.
//  2. Flush through the gate.
//  3. Assert the underlying writer was not flushed: its handler may already have returned.
func TestAGoneClientIsNeverFlushed(t *testing.T) {
	gone, leave := context.WithCancel(context.Background())
	leave()
	underlying := &countingFlusher{}
	stream := newDrain(gone, time.Minute).gate(underlying)

	stream.(http.Flusher).Flush()

	if underlying.flushes != 0 {
		t.Fatalf("Flush() through a departed client's gate reached the writer %d times, want 0", underlying.flushes)
	}
}
