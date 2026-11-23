package api

import (
	"cmp"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"devshard/cmd/gateway/engine"
)

const (
	firstEvent  = "data: {\"choices\":[{\"delta\":{\"content\":\"one\"}}]}\n\n"
	secondEvent = "data: {\"choices\":[{\"delta\":{\"content\":\"two\"},\"finish_reason\":\"stop\"}]}\n\n"
	doneEvent   = "data: [DONE]\n\n"
	fullStream  = firstEvent + secondEvent + doneEvent
	chatPath    = "/v1/chat/completions"
)

var replyChunks = []string{firstEvent, secondEvent, doneEvent}

type departingEngine struct {
	runs        atomic.Int64
	leaveAfter  int
	outcome     engine.RaceOutcome
	reportFirst bool

	delivered chan struct{}
	proceed   chan struct{}
	finished  chan struct{}
	announce  sync.Once
}

func newDepartingEngine(leaveAfter int, outcome engine.RaceOutcome) *departingEngine {
	return &departingEngine{
		leaveAfter: leaveAfter, outcome: outcome,
		delivered: make(chan struct{}), proceed: make(chan struct{}), finished: make(chan struct{}),
	}
}

func (e *departingEngine) Run(ctx context.Context, request engine.Request, client io.Writer) (engine.RaceOutcome, error) {
	e.runs.Add(1)
	request.OnEscrow("7")
	sent := 0
	var writeErr error
	for _, chunk := range replyChunks[:e.leaveAfter] {
		sent++
		if _, writeErr = client.Write([]byte(chunk)); writeErr != nil {
			break
		}
	}
	e.announce.Do(func() { close(e.delivered) })
	if writeErr == nil {
		<-ctx.Done()
	}
	background := func() {
		<-e.proceed
		sink := client.(engine.DepartedSink)
		for _, chunk := range replyChunks[sent:] {
			sink.WriteDeparted([]byte(chunk))
		}
		request.OnOutcome(e.outcome)
		request.OnFinished()
		close(e.finished)
	}
	if e.reportFirst {
		close(e.proceed)
		background()
	} else {
		go background()
	}
	return engine.RaceOutcome{EscrowID: "7"}, cmp.Or(writeErr, ctx.Err())
}

type headerSignal struct {
	*httptest.ResponseRecorder
	written chan struct{}
	once    sync.Once
}

func newHeaderSignal() *headerSignal {
	return &headerSignal{ResponseRecorder: httptest.NewRecorder(), written: make(chan struct{})}
}

func (h *headerSignal) WriteHeader(status int) {
	h.ResponseRecorder.WriteHeader(status)
	h.once.Do(func() { close(h.written) })
}

type brokenSocket struct {
	*httptest.ResponseRecorder
}

func (brokenSocket) Write([]byte) (int, error) { return 0, errors.New("socket closed") }

type adoptionFixture struct {
	live   *harness
	engine *departingEngine
}

func newAdoptionFixture(t *testing.T, leaveAfter int, outcome engine.RaceOutcome) *adoptionFixture {
	t.Helper()
	live := newHarness(t)
	var issued atomic.Int64
	live.server.requestIDs = func() string { return "request-" + strconv.FormatInt(issued.Add(1), 10) }
	fixture := &adoptionFixture{live: live, engine: newDepartingEngine(leaveAfter, outcome)}
	live.server.inference = fixture.engine
	return fixture
}

func (f *adoptionFixture) startAndLeave(t *testing.T, caller string) *httptest.ResponseRecorder {
	t.Helper()
	ctx, leave := context.WithCancel(t.Context())
	first := httptest.NewRecorder()
	returned := make(chan struct{})
	go func() {
		defer close(returned)
		f.live.requestIntoWithContext(t, ctx, first, http.MethodPost, chatPath, streamChatBody, callerHeaders(caller))
	}()
	<-f.engine.delivered
	leave()
	<-returned
	return first
}

func (f *adoptionFixture) startAdopter(t *testing.T, ctx context.Context) (*headerSignal, <-chan struct{}) {
	t.Helper()
	adopted := newHeaderSignal()
	served := make(chan struct{})
	go func() {
		defer close(served)
		f.live.requestIntoWithContext(t, ctx, adopted, http.MethodPost, chatPath, streamChatBody, callerHeaders("caller-a"))
	}()
	awaitValue(t, adopted.written, "the repeated request to adopt the stream")
	return adopted, served
}

func (f *adoptionFixture) requestOther(t *testing.T, caller, body string) *httptest.ResponseRecorder {
	t.Helper()
	other := httptest.NewRecorder()
	answered := make(chan struct{})
	go func() {
		defer close(answered)
		f.live.requestIntoWithContext(t, t.Context(), other, http.MethodPost, chatPath, body, callerHeaders(caller))
	}()
	awaitValue(t, answered, "the other request to be answered by a race of its own")
	return other
}

func (f *adoptionFixture) finishRace() {
	close(f.engine.proceed)
	<-f.engine.finished
}

func succeeded() engine.RaceOutcome { return engine.RaceOutcome{EscrowID: "7", Succeeded: true} }

// Test flow:
//  1. Start a streamed request whose client leaves after zero, one and two events while its race goes on.
//  2. Repeat the identical request, wait until it has taken the stream over, and let the race finish.
//  3. Assert the repeat received the whole reply from event 0 under the race's escrow, the first reply only what it had, and one race ran.
func TestARepeatedRequestAdoptsTheLiveStreamOfAClientThatLeft(t *testing.T) {
	for leaveAfter := range 3 {
		t.Run(strconv.Itoa(leaveAfter), func(t *testing.T) {
			fixture := newAdoptionFixture(t, leaveAfter, succeeded())
			first := fixture.startAndLeave(t, "caller-a")

			adopted, served := fixture.startAdopter(t, t.Context())
			fixture.finishRace()
			<-served

			if adopted.Body.String() != fullStream {
				t.Fatalf("adopted reply = %q, want %q", adopted.Body.String(), fullStream)
			}
			if got := adopted.Header().Get(EscrowHeader); got != "7" {
				t.Fatalf("adopted escrow = %q, want the race's escrow 7", got)
			}
			if got, want := first.Body.String(), joined(replyChunks[:leaveAfter]); got != want {
				t.Fatalf("first reply = %q, want only the %d events it received", got, leaveAfter)
			}
			if got := fixture.engine.runs.Load(); got != 1 {
				t.Fatalf("races = %d, want 1: an adopted stream spends no second nonce", got)
			}
		})
	}
}

func joined(chunks []string) string {
	var all string
	for _, chunk := range chunks {
		all += chunk
	}
	return all
}

// Test flow:
//  1. Start a streamed request whose client leaves after the first event, and let its race finish.
//  2. Repeat the identical request.
//  3. Assert the repeat was served the whole reply from the cache and no second race ran.
func TestARepeatedRequestAfterTheRaceEndedIsServedTheWholeReplyFromTheCache(t *testing.T) {
	fixture := newAdoptionFixture(t, 1, succeeded())
	fixture.startAndLeave(t, "caller-a")
	fixture.finishRace()

	repeated := fixture.live.request(t, http.MethodPost, chatPath, streamChatBody, callerHeaders("caller-a"))

	if repeated.Body.String() != fullStream {
		t.Fatalf("repeated reply = %q, want %q", repeated.Body.String(), fullStream)
	}
	if got := fixture.engine.runs.Load(); got != 1 {
		t.Fatalf("races = %d, want 1", got)
	}
}

// Test flow:
//  1. Start a streamed request whose client leaves after the first event, and report the race's outcome before the handler returns.
//  2. Repeat the identical request.
//  3. Assert the first reply holds only its first event, the repeat is the whole stream and no second race ran.
func TestAnOutcomeReportedBeforeTheHandlerReturnsStillStoresTheStream(t *testing.T) {
	fixture := newAdoptionFixture(t, 1, succeeded())
	fixture.engine.reportFirst = true
	first := fixture.startAndLeave(t, "caller-a")
	<-fixture.engine.finished

	if first.Body.String() != firstEvent {
		t.Fatalf("first reply = %q, want only %q: bytes after the client left must not reach its socket", first.Body.String(), firstEvent)
	}

	repeated := fixture.live.request(t, http.MethodPost, chatPath, streamChatBody, callerHeaders("caller-a"))

	if repeated.Body.String() != fullStream || fixture.engine.runs.Load() != 1 {
		t.Fatalf("repeat = %q after %d races, want the whole stream from the first race", repeated.Body.String(), fixture.engine.runs.Load())
	}
}

// Test flow:
//  1. Start a streamed request whose client leaves after the first event, and let the race fail.
//  2. Repeat the identical request against an engine that serves it whole.
//  3. Assert nothing was stored, so the repeat started a race of its own and was served by it.
func TestAFailedRaceOfAClientThatLeftIsNotStored(t *testing.T) {
	fixture := newAdoptionFixture(t, 1, engine.RaceOutcome{EscrowID: "7"})
	fixture.startAndLeave(t, "caller-a")
	fixture.finishRace()
	fixture.live.inference.chunks = replyChunks
	fixture.live.server.inference = fixture.live.inference

	repeated := fixture.live.request(t, http.MethodPost, chatPath, streamChatBody, callerHeaders("caller-a"))

	if repeated.Body.String() != fullStream || fixture.live.inference.runs.Load() != 1 {
		t.Fatalf("repeat = %q after %d races of its own, want a fresh race to serve it", repeated.Body.String(), fixture.live.inference.runs.Load())
	}
}

// Test flow:
//  1. Start a streamed request of one caller whose client leaves after the first event.
//  2. Send the identical body as another caller against an engine that serves it whole.
//  3. Assert the other caller started a race of its own, received its own reply, and the first stream was not taken over.
func TestAnotherCallerDoesNotAdoptTheStream(t *testing.T) {
	fixture := newAdoptionFixture(t, 1, succeeded())
	fixture.startAndLeave(t, "caller-a")
	fixture.live.inference.chunks = replyChunks
	fixture.live.server.inference = fixture.live.inference

	other := fixture.requestOther(t, "caller-b", streamChatBody)
	fixture.live.server.inference = fixture.engine
	owner, served := fixture.startAdopter(t, t.Context())
	fixture.finishRace()
	<-served

	if other.Body.String() != fullStream || fixture.live.inference.runs.Load() != 1 {
		t.Fatalf("other caller read %q after %d races of its own, want its own whole reply", other.Body.String(), fixture.live.inference.runs.Load())
	}
	if owner.Body.String() != fullStream || fixture.engine.runs.Load() != 1 {
		t.Fatalf("first caller's repeat read %q after %d races, want the stream of its first race", owner.Body.String(), fixture.engine.runs.Load())
	}
}

// Test flow:
//  1. Start a streamed request whose client leaves after the first event.
//  2. Adopt the stream with a client that leaves before the race ends, then repeat the request once more and let the race finish.
//  3. Assert the last repeat received the whole reply and only one race ran.
func TestAStreamWhoseAdopterLeftCanBeAdoptedAgain(t *testing.T) {
	fixture := newAdoptionFixture(t, 1, succeeded())
	fixture.startAndLeave(t, "caller-a")
	ctx, leave := context.WithCancel(t.Context())
	_, leftServed := fixture.startAdopter(t, ctx)
	leave()
	<-leftServed

	again, served := fixture.startAdopter(t, t.Context())
	fixture.finishRace()
	<-served

	if again.Body.String() != fullStream || fixture.engine.runs.Load() != 1 {
		t.Fatalf("second adopter read %q after %d races, want the whole stream from the first race", again.Body.String(), fixture.engine.runs.Load())
	}
}

// Test flow:
//  1. Start a streamed request whose socket refuses the first event while its request context stays live, and keep the race going.
//  2. Repeat the identical request, wait until it has taken the stream over, and let the race finish.
//  3. Assert the repeat received the whole reply from event 0 and only one race ran.
func TestARepeatedRequestAdoptsTheStreamOfASocketThatFailed(t *testing.T) {
	fixture := newAdoptionFixture(t, 1, succeeded())
	returned := make(chan struct{})
	go func() {
		defer close(returned)
		fixture.live.requestIntoWithContext(t, t.Context(), brokenSocket{httptest.NewRecorder()}, http.MethodPost, chatPath, streamChatBody, callerHeaders("caller-a"))
	}()
	<-returned

	adopted, served := fixture.startAdopter(t, t.Context())
	fixture.finishRace()
	<-served

	if adopted.Body.String() != fullStream || fixture.engine.runs.Load() != 1 {
		t.Fatalf("adopted reply = %q after %d races, want the whole stream from the first race", adopted.Body.String(), fixture.engine.runs.Load())
	}
}

// Test flow:
//  1. Start a streamed request whose client leaves after the first event.
//  2. Send the same caller's request with another body against an engine that serves it whole.
//  3. Assert the other body started a race of its own and received its own reply, while the first race ran once.
func TestAnotherBodyDoesNotAdoptTheStream(t *testing.T) {
	fixture := newAdoptionFixture(t, 1, succeeded())
	fixture.startAndLeave(t, "caller-a")
	fixture.live.inference.chunks = replyChunks
	fixture.live.server.inference = fixture.live.inference

	other := fixture.requestOther(t, "caller-a", strings.Replace(streamChatBody, `"hi"`, `"bye"`, 1))

	fixture.finishRace()

	if other.Body.String() != fullStream || fixture.live.inference.runs.Load() != 1 || fixture.engine.runs.Load() != 1 {
		t.Fatalf("other body read %q after %d races of its own and %d of the first, want a race of its own", other.Body.String(), fixture.live.inference.runs.Load(), fixture.engine.runs.Load())
	}
}
