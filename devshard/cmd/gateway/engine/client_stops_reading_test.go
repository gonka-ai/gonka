package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"devshard/cmd/gateway/limits"
)

type stuckClient struct {
	entered chan struct{}
	release chan struct{}
}

func (c *stuckClient) Write(chunk []byte) (int, error) {
	select {
	case c.entered <- struct{}{}:
	default:
	}
	<-c.release
	return len(chunk), nil
}

// Test flow:
//  1. Start a race whose only host receipts and streams content, against a client writer that blocks forever on its first write, as a socket whose peer stopped reading does.
//  2. Run the race with a clock that passes the hard timeout.
//  3. Assert runRace returns while the attempt goroutine is still blocked inside the client's Write.
func TestARaceReturnsWhileItsWinnerIsStillBlockedWritingToTheClient(t *testing.T) {
	client := &stuckClient{entered: make(chan struct{}, 1), release: make(chan struct{})}
	t.Cleanup(func() { close(client.release) })
	fixture := newRaceFixture(settledPolicy(), 1)
	fixture.request.Client = client
	fixture.host(60, 0, "host-0", &hostScript{receipt: true, chunks: []string{contentChunk(60), contentChunk(60)}, hold: true})
	fixture.clock.step = 30 * time.Minute
	fixture.deps.CancelGrace = 10 * time.Millisecond

	ran := make(chan error, 1)
	go func() {
		_, err := fixture.run(context.Background())
		ran <- err
	}()
	waitForValue(t, client.entered, "the winner's first write to the client")
	waitForValue(t, ran, "runRace returning while the winner was blocked writing to the client")
	select {
	case <-client.entered:
		t.Fatal("a second write reached the client")
	default:
	}
}

type expiringClient struct {
	recordingSink
	accept  int
	expire  context.CancelFunc
	expired int
}

func (c *expiringClient) Write(chunk []byte) (int, error) {
	if c.accept > 0 {
		c.accept--
		return c.recordingSink.Write(chunk)
	}
	c.expired++
	if c.expire != nil {
		c.expire()
	}
	return 0, os.ErrDeadlineExceeded
}

// Test flow:
//  1. Start a race whose only host receipts and streams three content chunks, against a client that takes the first chunk and then fails every write on its armed deadline, cancelling the request context inside the failing write as net/http does on a connection write error.
//  2. Run the race.
//  3. Assert one outcome is reported whose only attempt streamed all three chunks, won, is confirmed and finished, and judged a success with no deadline missed.
//  4. Assert no window verdict reached the limiter during the race and the host slot was released once.
//  5. Assert the client kept only the first chunk and saw one failed write.
func TestAClientWriteThatFailsOnItsDeadlineLeavesTheHostUncharged(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	client := &expiringClient{accept: 1, expire: cancel}
	fixture := newRaceFixture(settledPolicy(), 1)
	fixture.deps.DrainTimeout = time.Hour
	fixture.request.Client = client
	fixture.host(200, 0, "host-0", &hostScript{
		receipt:   true,
		chunks:    []string{contentChunk(200), contentChunk(200), contentChunk(200)},
		confirmed: true,
		finished:  true,
	})

	ran := make(chan error, 1)
	go func() {
		_, err := fixture.run(ctx)
		ran <- err
	}()
	if err := waitForValue(t, ran, "runRace returning"); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("runRace error = %v, want nil or context.Canceled", err)
	}
	reported := waitForValue(t, fixture.reported, "the race outcome being reported")
	if len(fixture.reported) != 0 || len(reported.Attempts) != 1 {
		t.Fatalf("outcomes = %d attempts = %d, want 1/1", 1+len(fixture.reported), len(reported.Attempts))
	}
	winner := reported.Attempts[0]
	if winner.ContentChunks != 3 || winner.Terminal != TerminalWon || reported.WinnerNonce != 200 {
		t.Fatalf("content chunks %d terminal %v winner %d, want 3/won/200", winner.ContentChunks, winner.Terminal, reported.WinnerNonce)
	}
	if !winner.NonceFinished || !winner.Confirmed {
		t.Fatalf("nonce finished = %v confirmed = %v, want both", winner.NonceFinished, winner.Confirmed)
	}
	if verdict, moves := reported.Verdict(winner); !moves || verdict != limits.Success {
		t.Fatalf("verdict = %v (moves %v), want %v", verdict, moves, limits.Success)
	}
	if winner.ReceiptDeadlineMissed || winner.FirstTokenDeadlineMissed {
		t.Fatalf("receipt/first-token deadline missed = %v/%v, want neither", winner.ReceiptDeadlineMissed, winner.FirstTokenDeadlineMissed)
	}
	if moves := len(fixture.limiter.verdicts); moves != 0 {
		t.Fatalf("window verdicts during the race = %d, want 0", moves)
	}
	assertOneSlotPerAttempt(t, fixture, 1)
	if got := len(client.writes); got != 1 {
		t.Fatalf("chunks the client kept = %d, want 1", got)
	}
	if client.expired != 1 {
		t.Fatalf("failed client writes = %d, want 1", client.expired)
	}
}

// Test flow:
//  1. Start a race whose only host receipts, streams three content chunks and would return the first write error it saw, held after its first chunk, against a client that fails every write on its armed deadline, with the request context left live.
//  2. Wait for runRace to return while the host is still held, then release the host.
//  3. Assert one outcome is reported whose only attempt won, and runRace returned the client's write error as its departure.
//  4. Assert the outcome is recorded as the client gone and not as a refused reply, and its attempt streamed all three chunks, is confirmed and finished, and is judged a success that denies no crown and plans no timeout vote.
//  5. Assert no window verdict reached the limiter during the race, the host slot was released once, and the client saw one failed write.
func TestAFailedClientWriteDepartsTheClientAndLeavesTheHostUncharged(t *testing.T) {
	client := &expiringClient{}
	streaming := make(chan uint64, 1)
	resume := make(chan struct{})
	fixture := newRaceFixture(settledPolicy(), 1)
	fixture.deps.DrainTimeout = time.Hour
	fixture.request.Client = client
	fixture.host(200, 0, "host-0", &hostScript{
		receipt:   true,
		chunks:    []string{contentChunk(200), contentChunk(200), contentChunk(200)},
		streaming: streaming,
		resume:    resume,
		confirmed: true,
		finished:  true,
	})

	ran := make(chan error, 1)
	go func() {
		_, err := fixture.run(context.Background())
		ran <- err
	}()
	err := waitForValue(t, ran, "runRace returning while the host is held")
	close(resume)
	reported := waitForValue(t, fixture.reported, "the race outcome being reported")
	if len(fixture.reported) != 0 || len(reported.Attempts) != 1 {
		t.Fatalf("outcomes = %d attempts = %d, want 1/1", 1+len(fixture.reported), len(reported.Attempts))
	}
	if terminal := reported.Attempts[0].Terminal; terminal != TerminalWon {
		t.Fatalf("terminal = %v, want %v", terminal, TerminalWon)
	}
	if !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("runRace error = %v, want the client's write error %v", err, os.ErrDeadlineExceeded)
	}
	if !reported.Lifecycle.ClientGone || reported.Lifecycle.ReplyRefused {
		t.Fatalf("lifecycle = %+v, want the client gone and no refused reply", reported.Lifecycle)
	}
	winner := reported.Attempts[0]
	if winner.ContentChunks != 3 || reported.WinnerNonce != 200 {
		t.Fatalf("content chunks %d winner %d, want 3/200", winner.ContentChunks, reported.WinnerNonce)
	}
	if !winner.NonceFinished || !winner.Confirmed {
		t.Fatalf("nonce finished = %v confirmed = %v, want both", winner.NonceFinished, winner.Confirmed)
	}
	if verdict, moves := reported.Verdict(winner); !moves || verdict != limits.Success {
		t.Fatalf("verdict = %v (moves %v), want %v", verdict, moves, limits.Success)
	}
	if reported.DeniesCrowning(winner) {
		t.Fatal("the host was struck for denying the crown")
	}
	if plan := reported.TimeoutPlan(); len(plan) != 0 {
		t.Fatalf("timeout plan = %v, want none", plan)
	}
	if moves := len(fixture.limiter.verdicts); moves != 0 {
		t.Fatalf("window verdicts during the race = %d, want 0", moves)
	}
	assertOneSlotPerAttempt(t, fixture, 1)
	if client.expired != 1 {
		t.Fatalf("failed client writes = %d, want 1", client.expired)
	}
}

var errReplyBufferFull = errors.New("reply buffer full")

type refusingClient struct{ refusals int }

func (c *refusingClient) Write([]byte) (int, error) {
	c.refusals++
	return 0, fmt.Errorf("%w: %w", ErrReplyRefused, errReplyBufferFull)
}

// Test flow:
//  1. Start a race whose only host receipts, streams three content chunks and finishes its nonce, held after its first chunk, against a client writer that refuses every chunk with an error wrapping `ErrReplyRefused`, as the API's writer does when its reply buffer is full, with the request context left live.
//  2. Wait for runRace to return while the host is held, then release the host.
//  3. Assert runRace returned the refusal and the one reported outcome's only attempt won.
//  4. Assert the outcome is recorded as a refused reply and not as the client gone, and the writer saw one refused chunk.
//  5. Assert the winner is labelled as a winner whose client never received the reply.
func TestAGatewayRefusalOfTheReplyIsNotRecordedAsTheClientLeaving(t *testing.T) {
	client := &refusingClient{}
	streaming := make(chan uint64, 1)
	resume := make(chan struct{})
	fixture := newRaceFixture(settledPolicy(), 1)
	fixture.deps.DrainTimeout = time.Hour
	fixture.request.Client = client
	fixture.host(210, 0, "host-0", &hostScript{
		receipt:   true,
		chunks:    []string{contentChunk(210), contentChunk(210), contentChunk(210)},
		streaming: streaming,
		resume:    resume,
		confirmed: true,
		finished:  true,
	})

	ran := make(chan error, 1)
	go func() {
		_, err := fixture.run(context.Background())
		ran <- err
	}()
	err := waitForValue(t, ran, "runRace returning while the host is held")
	close(resume)
	reported := waitForValue(t, fixture.reported, "the race outcome being reported")
	if !errors.Is(err, ErrReplyRefused) || !errors.Is(err, errReplyBufferFull) {
		t.Fatalf("runRace error = %v, want the refusal wrapping %v", err, errReplyBufferFull)
	}
	if len(reported.Attempts) != 1 || reported.Attempts[0].Terminal != TerminalWon {
		t.Fatalf("attempts = %+v, want one that won", reported.Attempts)
	}
	if !reported.Lifecycle.ReplyRefused || reported.Lifecycle.ClientGone {
		t.Fatalf("lifecycle = %+v, want a refused reply and the client not gone", reported.Lifecycle)
	}
	if client.refusals != 1 {
		t.Fatalf("refused chunks = %d, want 1", client.refusals)
	}
	if got := reported.Labels(reported.Attempts[0]).Visibility; got != VisibilityWinnerClientGone {
		t.Fatalf("winner visibility = %q, want %q", got, VisibilityWinnerClientGone)
	}
}
