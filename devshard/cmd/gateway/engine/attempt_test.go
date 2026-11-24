package engine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"devshard/cmd/gateway/internal/leakcheck"
	"devshard/cmd/gateway/limits"
	"devshard/cmd/gateway/scheduler"
	"devshard/transport"
)

func TestMain(m *testing.M) {
	leakcheck.VerifyTestMain(m)
}

type fakePrepared struct {
	nonce   uint64
	hostIdx int
}

func (p fakePrepared) Nonce() uint64 { return p.nonce }
func (p fakePrepared) HostIdx() int  { return p.hostIdx }

// hostSlot stands in for the release the scheduler hands the attempt with its assignment.
type hostSlot struct {
	releases int
}

func (s *hostSlot) release() { s.releases++ }

type fakeClassifier struct {
	perChunk []chunkFacts
	tail     chunkFacts
	seen     int
	releases int
}

func (c *fakeClassifier) Classify(chunk []byte) chunkFacts {
	facts := chunkFacts{}
	if c.seen < len(c.perChunk) {
		facts = c.perChunk[c.seen]
	}
	c.seen++
	return facts
}

func (c *fakeClassifier) Flush() chunkFacts { return c.tail }
func (c *fakeClassifier) Release()          { c.releases++ }

type fakeResponse struct {
	confirmed   bool
	confirmedAt int64
	bytesRead   int64
}

func (r fakeResponse) Confirmed() bool    { return r.confirmed }
func (r fakeResponse) ConfirmedAt() int64 { return r.confirmedAt }
func (r fakeResponse) StreamBytes() int64 { return r.bytesRead }
func (r fakeResponse) ReleaseFinish()     {}

// fakeDispatcher runs a script in place of a host: it writes the scripted chunks, optionally announcing a receipt first, then returns the scripted reply.
type fakeDispatcher struct {
	receipt  bool
	chunks   []string
	response Response
	err      error
	calls    int
	catchUp  func() error
}

func (d *fakeDispatcher) CatchUp(context.Context, scheduler.Prepared, func()) error {
	if d.catchUp == nil {
		return nil
	}
	return d.catchUp()
}

func (d *fakeDispatcher) Send(ctx context.Context, nonce scheduler.Prepared, stream io.Writer, onReceipt func()) (Response, error) {
	d.calls++
	if d.receipt {
		onReceipt()
	}
	for _, chunk := range d.chunks {
		if _, err := stream.Write([]byte(chunk)); err != nil {
			return d.response, err
		}
	}
	return d.response, d.err
}

type attemptFixture struct {
	spec       AttemptSpec
	slot       *hostSlot
	classifier *fakeClassifier
	dispatch   *fakeDispatcher
	sink       *bytes.Buffer
	events     chan AttemptEvent
}

func newAttemptFixture(dispatch *fakeDispatcher, classifier *fakeClassifier) *attemptFixture {
	slot := &hostSlot{}
	sink := &bytes.Buffer{}
	events := make(chan AttemptEvent, 64)
	tick := testEpoch
	return &attemptFixture{
		slot:       slot,
		classifier: classifier,
		dispatch:   dispatch,
		sink:       sink,
		events:     events,
		spec: AttemptSpec{
			Escrow:      "escrow-1",
			Model:       testModel,
			Participant: testParticipant,
			HostIdx:     3,
			HostLabel:   "host-3",
			Role:        "primary",
			StartReason: "receipt_timeout",
			Nonce:       fakePrepared{nonce: 77, hostIdx: 3},
			Dispatch:    dispatch,
			ReleaseSlot: slot.release,
			Classifier:  classifier,
			Sink:        sink,
			Events:      events,
			Now: func() time.Time {
				tick = tick.Add(time.Millisecond)
				return tick
			},
		},
	}
}

func (f *attemptFixture) drain() []AttemptEvent {
	close(f.events)
	collected := make([]AttemptEvent, 0, len(f.events))
	for event := range f.events {
		collected = append(collected, event)
	}
	return collected
}

func kindsOf(events []AttemptEvent) []AttemptEventKind {
	kinds := make([]AttemptEventKind, 0, len(events))
	for _, event := range events {
		kinds = append(kinds, event.Kind)
	}
	return kinds
}

func doneEvent(t *testing.T, events []AttemptEvent) AttemptEvent {
	t.Helper()
	for _, event := range events {
		if event.Kind == AttemptDone {
			return event
		}
	}
	t.Fatalf("no AttemptDone in %v", kindsOf(events))
	return AttemptEvent{}
}

func contentFacts(source string) chunkFacts {
	return chunkFacts{Content: true, ContentSource: source}
}

// Test flow:
//  1. Configure a `fakeDispatcher` that announces a receipt and writes two chunks, the second carrying content, and a `fakeClassifier` that reports content only on that second chunk.
//  2. Drain `fixture.events` concurrently while running `runAttempt`, so -race exercises the same handover the coordinator performs.
//  3. Assert the event kinds arrive in order: Dispatched, Receipt, FirstToken, Content, Chunk, Done: the role chunk stamps the first token and moves no stall clock.
//  4. Assert each event's timestamp is not before the previous one's.
//  5. Assert the AttemptDone outcome carries TerminalLost, the attempt's identity (nonce, host index, participant), one content chunk, Confirmed true with ContentSource "delta.content", and non-zero receipt/first-token/completed timestamps.
//  6. Assert the sink received both chunks verbatim.
func TestRunAttempt_HealthyAttemptEmitsEveryEventInOrder(t *testing.T) {
	t.Parallel()

	dispatch := &fakeDispatcher{
		receipt:  true,
		chunks:   []string{"data: role\n\n", "data: hello\n\n"},
		response: fakeResponse{confirmed: true, bytesRead: 512},
	}
	fixture := newAttemptFixture(dispatch, &fakeClassifier{perChunk: []chunkFacts{{}, contentFacts("delta.content")}})

	observed := make(chan []AttemptEvent, 1)
	go func() {
		collected := []AttemptEvent{}
		for event := range fixture.events {
			collected = append(collected, event)
		}
		observed <- collected
	}()

	runAttempt(context.Background(), fixture.spec)
	close(fixture.events)
	events := <-observed

	want := []AttemptEventKind{
		AttemptDispatched,
		AttemptReceipt,
		AttemptFirstToken,
		AttemptContent,
		AttemptChunk,
		AttemptDone,
	}
	if got := kindsOf(events); !equalKinds(got, want) {
		t.Fatalf("event kinds = %v, want %v", got, want)
	}

	for index := 1; index < len(events); index++ {
		if events[index].At.Before(events[index-1].At) {
			t.Fatalf("event %d timestamp %v precedes %v", index, events[index].At, events[index-1].At)
		}
	}

	done := doneEvent(t, events)
	outcome := done.Outcome
	switch {
	case outcome == nil:
		t.Fatal("AttemptDone carried no outcome")
	case outcome.Terminal != TerminalLost:
		t.Fatalf("terminal = %v, want TerminalLost", outcome.Terminal)
	case outcome.Nonce != 77 || outcome.HostIdx != 3 || outcome.Participant != testParticipant:
		t.Fatalf("identity not carried: %+v", outcome)
	case outcome.ContentChunks != 1:
		t.Fatalf("content chunks = %d, want 1", outcome.ContentChunks)
	case !outcome.Confirmed || outcome.ContentSource != "delta.content":
		t.Fatalf("response facts not carried: %+v", outcome)
	case outcome.ReceiptTime.IsZero() || outcome.FirstToken.IsZero() || outcome.Completed.IsZero():
		t.Fatalf("timestamps not stamped: %+v", outcome)
	}
	if fixture.sink.String() != "data: role\n\ndata: hello\n\n" {
		t.Fatalf("sink = %q", fixture.sink.String())
	}
}

func equalKinds(got, want []AttemptEventKind) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}

func statusError(path string, code int, body string) error {
	return fmt.Errorf("send: %w", &transport.UpstreamStatusError{Path: path, StatusCode: code, Body: body})
}

// Test flow:
//  1. Build a table of dispatcher/classifier combinations covering: streamed content, a receipt with nothing after it, content only in the unflushed tail, tokens burned on an empty stream, an SSE error event, a capability refusal, a stream with no receipt at all, each HTTP status code the gateway distinguishes (429/503/403/404/401 with and without timestamp drift/400/500), an escrow-not-found 500, a failure off the inference path, a truncated SSE stream, an oversized SSE event or body, an unexpected or clean EOF, a dial failure, a context-cancelled dispatch, and a post-state-root divergence.
//  2. Run `runAttempt`, cancelling the context first for the cases that need it.
//  3. Assert the AttemptDone outcome's Terminal matches the case's want.
//  4. Assert StateDivergent and the lifecycle's EscrowMissing match the case's wantDivergent and wantEscrowGone.
func TestRunAttempt_TerminalClassification(t *testing.T) {
	t.Parallel()

	const inferencePath = "/v1/devshard/chat/completions"

	testCases := []struct {
		name           string
		cancelled      bool
		dispatch       *fakeDispatcher
		classifier     *fakeClassifier
		want           Terminal
		wantDivergent  bool
		wantEscrowGone bool
	}{
		{
			name:       "content streamed",
			dispatch:   &fakeDispatcher{receipt: true, chunks: []string{"a"}, response: fakeResponse{confirmed: true}},
			classifier: &fakeClassifier{perChunk: []chunkFacts{contentFacts("delta.content")}},
			want:       TerminalLost,
		},
		{
			name:       "receipt then nothing",
			dispatch:   &fakeDispatcher{receipt: true, chunks: []string{"data: [DONE]\n\n"}, response: fakeResponse{confirmed: true}},
			classifier: &fakeClassifier{},
			want:       TerminalEmptyStream,
		},
		{
			name:       "content only in the newline-less tail",
			dispatch:   &fakeDispatcher{receipt: true, chunks: []string{"data: {"}, response: fakeResponse{confirmed: true}},
			classifier: &fakeClassifier{tail: contentFacts("delta.content")},
			want:       TerminalLost,
		},
		{
			name:       "empty stream that burned completion tokens",
			dispatch:   &fakeDispatcher{receipt: true, chunks: []string{"data: [DONE]\n\n"}, response: fakeResponse{confirmed: true}},
			classifier: &fakeClassifier{perChunk: []chunkFacts{{UsageCompletionTokens: 40, TokensBurned: true}}},
			want:       TerminalBurnEmpty,
		},
		{
			name:     "openai error event",
			dispatch: &fakeDispatcher{receipt: true, chunks: []string{"data: err\n\n"}, response: fakeResponse{confirmed: true}},
			classifier: &fakeClassifier{perChunk: []chunkFacts{{
				Error: true, ErrorSource: "sse", ErrorCode: "500", ErrorType: "server_error", ErrorMessage: "boom",
			}}},
			want: TerminalErrorStream,
		},
		{
			name:     "capability refusal another host can serve",
			dispatch: &fakeDispatcher{receipt: true, chunks: []string{"data: err\n\n"}, response: fakeResponse{confirmed: true}},
			classifier: &fakeClassifier{perChunk: []chunkFacts{{
				ErrorSource: "sse", ErrorMessage: "maximum context length is 8192",
				CapabilityRefused: true,
			}}},
			want: TerminalCapabilityRefused,
		},
		{
			name:       "stream completed without a receipt",
			dispatch:   &fakeDispatcher{chunks: []string{"a"}, response: fakeResponse{}},
			classifier: &fakeClassifier{perChunk: []chunkFacts{contentFacts("delta.content")}},
			want:       TerminalNoReceipt,
		},
		{
			name:       "http 429",
			dispatch:   &fakeDispatcher{err: statusError(inferencePath, 429, "")},
			classifier: &fakeClassifier{},
			want:       TerminalThrottled,
		},
		{
			name: "http 429 for a spent escrow lookup budget",
			dispatch: &fakeDispatcher{err: &transport.UpstreamStatusError{
				Path: inferencePath, StatusCode: http.StatusTooManyRequests, DevshardError: transport.DevshardErrorEscrowLookupLimited,
			}},
			classifier: &fakeClassifier{},
			want:       TerminalEscrowLookupLimited,
		},
		{
			name:       "http 503",
			dispatch:   &fakeDispatcher{err: statusError(inferencePath, 503, "")},
			classifier: &fakeClassifier{},
			want:       TerminalUnavailable,
		},
		{
			name:       "http 403",
			dispatch:   &fakeDispatcher{err: statusError(inferencePath, 403, "")},
			classifier: &fakeClassifier{},
			want:       TerminalForbidden,
		},
		{
			name:       "http 404",
			dispatch:   &fakeDispatcher{err: statusError(inferencePath, 404, "")},
			classifier: &fakeClassifier{},
			want:       TerminalNotFound,
		},
		{
			name:       "http 401 timestamp drift",
			dispatch:   &fakeDispatcher{err: statusError(inferencePath, 401, "Timestamp Drift detected")},
			classifier: &fakeClassifier{},
			want:       TerminalTimestampDrift,
		},
		{
			name:       "http 401 without drift",
			dispatch:   &fakeDispatcher{err: statusError(inferencePath, 401, "bad signature")},
			classifier: &fakeClassifier{},
			want:       TerminalRejected,
		},
		{
			name:       "http 400",
			dispatch:   &fakeDispatcher{err: statusError(inferencePath, 400, "bad request")},
			classifier: &fakeClassifier{},
			want:       TerminalRejected,
		},
		{
			name:       "http 500",
			dispatch:   &fakeDispatcher{err: statusError(inferencePath, 500, "internal")},
			classifier: &fakeClassifier{},
			want:       TerminalUpstreamServerError,
		},
		{
			name:           "escrow missing is reported not blamed",
			dispatch:       &fakeDispatcher{err: statusError(inferencePath, 500, "escrow not found")},
			classifier:     &fakeClassifier{},
			want:           TerminalRejected,
			wantEscrowGone: true,
		},
		{
			name:       "failure off the inference path",
			dispatch:   &fakeDispatcher{err: statusError("/v1/devshard/gossip/diffs", 500, "")},
			classifier: &fakeClassifier{},
			want:       TerminalOffPath,
		},
		{
			name:       "sse stream truncated",
			dispatch:   &fakeDispatcher{receipt: true, err: fmt.Errorf("read: %w", transport.ErrSSEStreamTruncated)},
			classifier: &fakeClassifier{},
			want:       TerminalStreamTruncated,
		},
		{
			name:       "an sse event past the cap",
			dispatch:   &fakeDispatcher{err: fmt.Errorf("read: %w", transport.ErrSSEEventTooLarge)},
			classifier: &fakeClassifier{},
			want:       TerminalResponseTooLarge,
		},
		{
			name:       "a non-stream body past the cap",
			dispatch:   &fakeDispatcher{err: fmt.Errorf("read: %w", transport.ErrResponseBodyTooLarge)},
			classifier: &fakeClassifier{},
			want:       TerminalResponseTooLarge,
		},
		{
			name:       "unexpected eof",
			dispatch:   &fakeDispatcher{err: fmt.Errorf("read: %w", io.ErrUnexpectedEOF)},
			classifier: &fakeClassifier{},
			want:       TerminalUnexpectedEOF,
		},
		{
			name:       "clean eof",
			dispatch:   &fakeDispatcher{err: fmt.Errorf("read: %w", io.EOF)},
			classifier: &fakeClassifier{},
			want:       TerminalUnexpectedEOF,
		},
		{
			name:       "dial failure",
			dispatch:   &fakeDispatcher{err: errors.New("dial tcp: connection refused")},
			classifier: &fakeClassifier{},
			want:       TerminalDialFailure,
		},
		{
			name:       "cut by the coordinator",
			cancelled:  true,
			dispatch:   &fakeDispatcher{err: context.Canceled},
			classifier: &fakeClassifier{},
			want:       TerminalClientCancelled,
		},
		{
			name:          "post state root divergence",
			dispatch:      &fakeDispatcher{err: fmt.Errorf("apply: %w", ErrStateRootDivergence)},
			classifier:    &fakeClassifier{},
			want:          TerminalDialFailure,
			wantDivergent: true,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			fixture := newAttemptFixture(testCase.dispatch, testCase.classifier)
			ctx := context.Background()
			if testCase.cancelled {
				cancellable, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancellable
			}

			runAttempt(ctx, fixture.spec)
			done := doneEvent(t, fixture.drain())

			if done.Outcome.Terminal != testCase.want {
				t.Fatalf("terminal = %v, want %v", done.Outcome.Terminal, testCase.want)
			}
			if done.Outcome.StateDivergent != testCase.wantDivergent {
				t.Fatalf("state divergent = %v, want %v", done.Outcome.StateDivergent, testCase.wantDivergent)
			}
			if done.Lifecycle.EscrowMissing != testCase.wantEscrowGone {
				t.Fatalf("escrow missing = %v, want %v", done.Lifecycle.EscrowMissing, testCase.wantEscrowGone)
			}
		})
	}
}

// Test flow:
//  1. Build a table of dispatch outcomes: an outright dial failure, a stream that fails mid-read after producing content, a cancelled context, and a clean completion.
//  2. Run `runAttempt` for each case and drain its events.
//  3. Assert the host slot's `release` was called exactly once.
//  4. Assert the classifier's `Release` was called exactly once.
func TestRunAttempt_ReleasesTheHostSlotOnEveryExitPath(t *testing.T) {
	t.Parallel()

	failingSink := &fakeDispatcher{
		receipt:  true,
		chunks:   []string{"a", "b"},
		response: fakeResponse{confirmed: true},
		err:      errors.New("mid-stream reset"),
	}

	testCases := []struct {
		name      string
		cancelled bool
		dispatch  *fakeDispatcher
	}{
		{
			name:     "dispatch failed outright",
			dispatch: &fakeDispatcher{err: errors.New("dial tcp: connection refused")},
		},
		{
			name:     "stream failed after content",
			dispatch: failingSink,
		},
		{
			name:      "context cancelled",
			cancelled: true,
			dispatch:  &fakeDispatcher{err: context.Canceled},
		},
		{
			name:     "clean completion",
			dispatch: &fakeDispatcher{receipt: true, chunks: []string{"a"}, response: fakeResponse{confirmed: true}},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			classifier := &fakeClassifier{perChunk: []chunkFacts{contentFacts("delta.content")}}
			fixture := newAttemptFixture(testCase.dispatch, classifier)
			ctx := context.Background()
			if testCase.cancelled {
				cancellable, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancellable
			}

			runAttempt(ctx, fixture.spec)
			fixture.drain()

			if fixture.slot.releases != 1 {
				t.Fatalf("host-slot releases = %d, want 1", fixture.slot.releases)
			}
			if classifier.releases != 1 {
				t.Fatalf("classifier releases = %d, want 1", classifier.releases)
			}
		})
	}
}

// Test flow:
//  1. Configure a dispatcher whose session catch-up reads the clock before the send.
//  2. Run the attempt.
//  3. Assert the Dispatched event and the outcome's send time come after the catch-up: the host's receipt deadline and the hedge are timed from when the request leaves, not from the session teaching it earlier nonces.
func TestRunAttempt_TheHostsClockStartsAfterTheSessionsCatchUp(t *testing.T) {
	t.Parallel()

	dispatch := &fakeDispatcher{receipt: true, chunks: []string{"data: hello\n\n"}, response: fakeResponse{confirmed: true}}
	fixture := newAttemptFixture(dispatch, &fakeClassifier{perChunk: []chunkFacts{contentFacts("delta.content")}})
	var caughtUpAt time.Time
	dispatch.catchUp = func() error {
		caughtUpAt = fixture.spec.Now()
		return nil
	}

	runAttempt(context.Background(), fixture.spec)
	events := fixture.drain()

	if caughtUpAt.IsZero() {
		t.Fatal("the session was never asked to catch the host up")
	}
	if events[0].Kind != AttemptDispatched || !events[0].At.After(caughtUpAt) {
		t.Fatalf("first event = %v at %v, want Dispatched after the catch-up at %v", events[0].Kind, events[0].At, caughtUpAt)
	}
	if sent := doneEvent(t, events).Outcome.SendTime; !sent.After(caughtUpAt) {
		t.Fatalf("outcome send time = %v, want after the catch-up at %v", sent, caughtUpAt)
	}
}

// Test flow:
//  1. Configure a dispatcher whose session catch-up fails.
//  2. Run the attempt.
//  3. Assert the request was never sent and the attempt ends on the catch-up's error, classified as a send failure is.
func TestRunAttempt_AFailedCatchUpEndsTheAttemptUnsent(t *testing.T) {
	t.Parallel()

	dispatch := &fakeDispatcher{catchUp: func() error { return errors.New("dial tcp: connection refused") }}
	fixture := newAttemptFixture(dispatch, &fakeClassifier{})

	runAttempt(context.Background(), fixture.spec)
	outcome := doneEvent(t, fixture.drain()).Outcome

	if dispatch.calls != 0 {
		t.Fatalf("sends = %d, want none after a failed catch-up", dispatch.calls)
	}
	if outcome.Terminal != TerminalDialFailure {
		t.Fatalf("terminal = %v, want %v", outcome.Terminal, TerminalDialFailure)
	}
}

// Test flow:
//  1. Configure a dispatcher that always fails, and override ReleaseSlot to record how many events were already queued the first time it runs.
//  2. Run `runAttempt` and drain the events.
//  3. Assert the last event is AttemptDone.
//  4. Assert the host slot was released while exactly the events before AttemptDone were queued, proving the slot comes back before AttemptDone is announced.
func TestRunAttempt_ReleasesTheHostSlotBeforeAnnouncingItIsDone(t *testing.T) {
	t.Parallel()
	fixture := newAttemptFixture(&fakeDispatcher{err: errors.New("503 service unavailable")}, &fakeClassifier{})
	eventsQueuedAtRelease := -1
	fixture.spec.ReleaseSlot = func() {
		if eventsQueuedAtRelease < 0 {
			eventsQueuedAtRelease = len(fixture.events)
		}
	}

	runAttempt(context.Background(), fixture.spec)
	events := fixture.drain()

	if last := events[len(events)-1].Kind; last != AttemptDone {
		t.Fatalf("last event = %v, want AttemptDone", last)
	}
	if eventsQueuedAtRelease != len(events)-1 {
		t.Fatalf("events queued when the host slot came back = %d, want %d: a slot returned after AttemptDone lets the verdict read the host as still carrying this attempt", eventsQueuedAtRelease, len(events)-1)
	}
}

// Test flow:
//  1. Build a table of dispatchers that send either no chunks at all or several contentless events ending in [DONE].
//  2. Run `runAttempt` with a classifier that reports no content for any chunk.
//  3. Assert the outcome's Terminal is TerminalEmptyStream.
//  4. Assert StreamChunks counts every chunk written, whether or not it carried content.
func TestRunAttemptCountsEveryChunkEvenWhenNoneCarriedContent(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name             string
		chunks           []string
		wantStreamChunks int64
	}{
		{name: "a host that sent nothing at all"},
		{
			name:             "a host that sent only empty events",
			chunks:           []string{"data: {}\n\n", "data: {}\n\n", "data: [DONE]\n\n"},
			wantStreamChunks: 3,
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			fixture := newAttemptFixture(
				&fakeDispatcher{receipt: true, chunks: testCase.chunks, response: fakeResponse{confirmed: true}},
				&fakeClassifier{},
			)

			runAttempt(context.Background(), fixture.spec)
			done := doneEvent(t, fixture.drain())

			if done.Outcome.Terminal != TerminalEmptyStream {
				t.Fatalf("terminal = %v, want TerminalEmptyStream", done.Outcome.Terminal)
			}
			if done.Outcome.StreamChunks != testCase.wantStreamChunks {
				t.Errorf("StreamChunks = %d, want %d", done.Outcome.StreamChunks, testCase.wantStreamChunks)
			}
		})
	}
}

// Test flow:
//  1. Configure a dispatcher that writes an empty event, then a tail event shaped like content but reported by the fake classifier as carrying nothing.
//  2. Run `runAttempt`.
//  3. Assert the outcome's LastChunkHead is exactly the tail chunk's bytes.
func TestRunAttempt_AnEmptyStreamCarriesTheHeadOfItsLastChunk(t *testing.T) {
	t.Parallel()
	tail := "data: {\"choices\":[{\"delta\":{\"content\":[{\"type\":\"text\"}]}}]}\n\n"
	fixture := newAttemptFixture(
		&fakeDispatcher{receipt: true, chunks: []string{"data: {}\n\n", tail}, response: fakeResponse{confirmed: true}},
		&fakeClassifier{},
	)

	runAttempt(context.Background(), fixture.spec)
	done := doneEvent(t, fixture.drain())

	if done.Outcome.LastChunkHead != tail {
		t.Fatalf("LastChunkHead = %q, want the last chunk %q", done.Outcome.LastChunkHead, tail)
	}
}

// Test flow:
//  1. Configure a dispatcher that writes an empty event, a second empty event, then the [DONE] terminator.
//  2. Run `runAttempt` with a classifier reporting no content.
//  3. Assert StreamChunks counts the terminator along with the other two chunks.
//  4. Assert LastChunkHead is the event before the terminator, not the terminator itself.
func TestRunAttempt_TheTerminatorIsNotWhatTheHeadKeeps(t *testing.T) {
	t.Parallel()
	last := "data: {\"choices\":[{\"delta\":{}}]}\n\n"
	fixture := newAttemptFixture(
		&fakeDispatcher{
			receipt:  true,
			chunks:   []string{"data: {}\n\n", last, "data: [DONE]\n\n"},
			response: fakeResponse{confirmed: true},
		},
		&fakeClassifier{},
	)

	runAttempt(context.Background(), fixture.spec)
	done := doneEvent(t, fixture.drain())

	if done.Outcome.StreamChunks != 3 {
		t.Fatalf("StreamChunks = %d, want the terminator counted with the rest", done.Outcome.StreamChunks)
	}
	if done.Outcome.LastChunkHead != last {
		t.Fatalf("LastChunkHead = %q, want the event before the terminator", done.Outcome.LastChunkHead)
	}
}

// Test flow:
//  1. Configure a dispatcher that writes an empty event followed by one carrying content, with a classifier that reports content only on the second.
//  2. Run `runAttempt`.
//  3. Assert the outcome's LastChunkHead is empty, since an answered stream keeps no diagnostic head.
func TestRunAttempt_AnAnsweredStreamCarriesNoChunkHead(t *testing.T) {
	t.Parallel()
	fixture := newAttemptFixture(
		&fakeDispatcher{receipt: true, chunks: []string{"data: {}\n\n", "data: hello\n\n"}, response: fakeResponse{confirmed: true}},
		&fakeClassifier{perChunk: []chunkFacts{{}, contentFacts("delta.content")}},
	)

	runAttempt(context.Background(), fixture.spec)
	done := doneEvent(t, fixture.drain())

	if done.Outcome.LastChunkHead != "" {
		t.Fatalf("LastChunkHead = %q, want none where the answer carried content", done.Outcome.LastChunkHead)
	}
}

// Test flow:
//  1. Configure a dispatcher that writes one oversized contentless chunk, several times past maxEmptyChunkLogged.
//  2. Run `runAttempt`.
//  3. Assert the kept LastChunkHead is exactly maxEmptyChunkLogged bytes long.
//  4. Assert that head is a prefix of the original oversized chunk.
func TestRunAttempt_AChunkPastTheCapIsCutToIt(t *testing.T) {
	t.Parallel()
	oversized := "data: " + strings.Repeat("x", 4*maxEmptyChunkLogged)
	fixture := newAttemptFixture(
		&fakeDispatcher{receipt: true, chunks: []string{oversized}, response: fakeResponse{confirmed: true}},
		&fakeClassifier{},
	)

	runAttempt(context.Background(), fixture.spec)
	done := doneEvent(t, fixture.drain())

	if len(done.Outcome.LastChunkHead) != maxEmptyChunkLogged {
		t.Fatalf("the kept head is %d bytes, want it cut to %d", len(done.Outcome.LastChunkHead), maxEmptyChunkLogged)
	}
	if !strings.HasPrefix(oversized, done.Outcome.LastChunkHead) {
		t.Fatal("the kept head is not the start of the chunk it came from")
	}
}

// Test flow:
//  1. Build a 503 `UpstreamStatusError` whose body has leading and trailing spaces.
//  2. Run `upstreamRefusal` on it.
//  3. Assert the returned status is the host's own status code.
//  4. Assert the returned body is the host's reason with whitespace trimmed.
func TestUpstreamRefusalKeepsTheHostsOwnWords(t *testing.T) {
	t.Parallel()

	status, body := upstreamRefusal(statusError("/v1/chat/completions", http.StatusServiceUnavailable, "  no healthy upstream  "))

	if status != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want the one the host answered with", status)
	}
	if body != "no healthy upstream" {
		t.Fatalf("body = %q, want the host's reason trimmed", body)
	}
}

// Test flow:
//  1. Build a 503 `UpstreamStatusError` whose body is three times longer than maxUpstreamBodyLogged.
//  2. Run `upstreamRefusal` on it.
//  3. Assert the returned body length is capped at maxUpstreamBodyLogged.
func TestUpstreamRefusalTruncatesALongBody(t *testing.T) {
	t.Parallel()

	_, body := upstreamRefusal(statusError("/v1/chat/completions", http.StatusServiceUnavailable,
		strings.Repeat("x", maxUpstreamBodyLogged*3)))

	if len(body) != maxUpstreamBodyLogged {
		t.Fatalf("body length = %d, want it capped at %d", len(body), maxUpstreamBodyLogged)
	}
}

// Test flow:
//  1. Run `upstreamRefusal` on a plain io.EOF, which carries no upstream status.
//  2. Assert the returned status is 0 and the body is empty.
func TestUpstreamRefusalIsEmptyForANonStatusError(t *testing.T) {
	t.Parallel()

	status, body := upstreamRefusal(io.EOF)

	if status != 0 || body != "" {
		t.Fatalf("got status %d body %q, want nothing for an error that carries no status", status, body)
	}
}

// Test flow:
//  1. Configure a dispatcher that fails with a 503 `UpstreamStatusError` carrying the host's own reason.
//  2. Run `runAttempt`.
//  3. Assert the outcome's Terminal is TerminalUnavailable.
//  4. Assert UpstreamStatus and UpstreamBody carry the host's status code and reason through to the outcome.
func TestRunAttempt_CarriesTheRefusalIntoTheOutcome(t *testing.T) {
	t.Parallel()

	fixture := newAttemptFixture(
		&fakeDispatcher{err: statusError("/v1/devshard/chat/completions", http.StatusServiceUnavailable, "no healthy upstream")},
		&fakeClassifier{})

	runAttempt(context.Background(), fixture.spec)
	done := doneEvent(t, fixture.drain())

	if done.Outcome.Terminal != TerminalUnavailable {
		t.Fatalf("terminal = %v, want %v", done.Outcome.Terminal, TerminalUnavailable)
	}
	if done.Outcome.UpstreamStatus != http.StatusServiceUnavailable {
		t.Fatalf("upstream status = %d, want the host's own", done.Outcome.UpstreamStatus)
	}
	if done.Outcome.UpstreamBody != "no healthy upstream" {
		t.Fatalf("upstream body = %q, want the host's reason", done.Outcome.UpstreamBody)
	}
}

// Test flow:
//  1. Configure a dispatcher that writes a content-shaped first chunk, then a usage-only chunk, then [DONE], with a classifier that reports no content for any of them.
//  2. Run `runAttempt`.
//  3. Assert FirstChunkHead is the first chunk (where a delta would have arrived).
//  4. Assert LastChunkHead is the usage chunk, not the terminator.
func TestRunAttempt_AnEmptyStreamCarriesTheHeadOfItsFirstChunkToo(t *testing.T) {
	t.Parallel()
	first := "data: {\"choices\":[{\"delta\":{\"content\":[{\"type\":\"text\"}]}}]}\n\n"
	usage := "data: {\"choices\":[],\"usage\":{\"completion_tokens\":0}}\n\n"
	fixture := newAttemptFixture(
		&fakeDispatcher{
			receipt:  true,
			chunks:   []string{first, usage, "data: [DONE]\n\n"},
			response: fakeResponse{confirmed: true},
		},
		&fakeClassifier{},
	)

	runAttempt(context.Background(), fixture.spec)
	done := doneEvent(t, fixture.drain())

	if done.Outcome.FirstChunkHead != first {
		t.Fatalf("FirstChunkHead = %q, want the chunk a delta would have arrived in", done.Outcome.FirstChunkHead)
	}
	if done.Outcome.LastChunkHead != usage {
		t.Fatalf("LastChunkHead = %q, want the usage event", done.Outcome.LastChunkHead)
	}
}

// Test flow:
//  1. Configure a dispatcher that writes exactly one contentless chunk, then [DONE].
//  2. Run `runAttempt`.
//  3. Assert LastChunkHead is that one chunk.
//  4. Assert FirstChunkHead is empty, since a single chunk that is both first and last is not repeated.
func TestRunAttempt_ASingleChunkIsOfferedOnceNotTwice(t *testing.T) {
	t.Parallel()
	only := "data: {\"choices\":[]}\n\n"
	fixture := newAttemptFixture(
		&fakeDispatcher{receipt: true, chunks: []string{only, "data: [DONE]\n\n"}, response: fakeResponse{confirmed: true}},
		&fakeClassifier{},
	)

	runAttempt(context.Background(), fixture.spec)
	done := doneEvent(t, fixture.drain())

	if done.Outcome.LastChunkHead != only {
		t.Fatalf("LastChunkHead = %q, want the only chunk", done.Outcome.LastChunkHead)
	}
	if done.Outcome.FirstChunkHead != "" {
		t.Fatalf("FirstChunkHead = %q, want none where it repeats the last", done.Outcome.FirstChunkHead)
	}
}

// Test flow:
//  1. Judge a host that refused with a spent escrow lookup budget beside one that throttled.
//  2. Assert the lookup refusal moves no congestion window and records no sample, while the throttle still narrows the window.
func TestAnEscrowLookupRefusalLeavesTheHostsWindowAlone(t *testing.T) {
	outcome := RaceOutcome{Model: "llama"}
	refused := failedAttempt(TerminalEscrowLookupLimited)
	throttled := failedAttempt(TerminalThrottled)

	if verdict, moves := outcome.Verdict(refused); moves && verdict != limits.ModelOutcome {
		t.Fatalf("Verdict(escrow lookup limited) = %v, want no overload on the host", verdict)
	}
	if _, exemption := outcome.Sample(refused); exemption != ExemptEscrowLookupLimited {
		t.Fatalf("Sample exemption = %v, want ExemptEscrowLookupLimited", exemption)
	}
	if verdict, moves := outcome.Verdict(throttled); !moves || verdict != limits.Overload {
		t.Fatalf("Verdict(throttled) = %v (moves %t), want an overload", verdict, moves)
	}
}

// Test flow:
//  1. Close the race's report channel, as a race that already reported does, with no room left for events.
//  2. Emit a terminal event carrying a held error-miss Finish.
//  3. Assert the emit returns instead of blocking, and the held Finish is released rather than pinned forever.
func TestAnAttemptOutlivingItsRaceReleasesItsHeldFinish(t *testing.T) {
	reported := make(chan struct{})
	close(reported)
	released := false
	spec := AttemptSpec{Events: make(chan AttemptEvent), Reported: reported}
	outcome := &AttemptOutcome{MissProof: &MissProof{releaseFinish: func() { released = true }}}

	done := make(chan struct{})
	go func() {
		defer close(done)
		spec.finish(AttemptEvent{Kind: AttemptDone, Outcome: outcome})
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("finish() = still blocked after 10s, want it to return once the race has reported")
	}
	if !released {
		t.Fatal("releaseHeldFinish() calls = 0, want 1: nobody is left to post the claim")
	}
}
