package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"devshard/cmd/gateway/config"
)

type stalledClient struct {
	*httptest.ResponseRecorder
	entered       chan struct{}
	release       chan struct{}
	releaseOnce   sync.Once
	deadlineArmed bool
	expiredWrites int
}

func (c *stalledClient) SetWriteDeadline(deadline time.Time) error {
	c.deadlineArmed = !deadline.IsZero()
	return nil
}

func (c *stalledClient) Write(chunk []byte) (int, error) {
	select {
	case c.entered <- struct{}{}:
	default:
	}
	<-c.release
	if c.deadlineArmed {
		c.expiredWrites++
		return 0, os.ErrDeadlineExceeded
	}
	return c.ResponseRecorder.Write(chunk)
}

func (c *stalledClient) releaseWrites() { c.releaseOnce.Do(func() { close(c.release) }) }

func serveToStalledClient(t *testing.T, live *harness, body string) (*stalledClient, <-chan struct{}) {
	t.Helper()
	client := &stalledClient{ResponseRecorder: httptest.NewRecorder(), entered: make(chan struct{}, 1), release: make(chan struct{})}
	t.Cleanup(client.releaseWrites)
	handled := make(chan struct{})
	go func() {
		defer close(handled)
		live.requestInto(t, client, http.MethodPost, "/v1/chat/completions", body, nil)
	}()
	return client, handled
}

func assertStalledWriteExpiredAndSlotReturned(t *testing.T, live *harness, client *stalledClient, handled <-chan struct{}) {
	t.Helper()
	awaitValue(t, client.entered, "a write to the client")
	client.releaseWrites()
	awaitValue(t, handled, "the handler returning")
	if client.expiredWrites == 0 {
		t.Error("no write to the client hit an armed deadline, want at least one")
	}
	if acquired, released := live.limiter.acquires.Load(), live.limiter.releases.Load(); acquired != 1 || released != 1 {
		t.Errorf("limiter acquires/releases = %d/%d, want 1/1", acquired, released)
	}
}

// Test flow:
//  1. Serve a streamed chat completion whose engine writes content chunks, to a client whose first write blocks until released and then fails only if a write deadline was armed.
//  2. Wait for the write to reach the client, then release it.
//  3. Assert the write failed on its armed deadline and the handler returned.
//  4. Assert the handler gave its limiter slot back.
func TestAClientThatStopsReadingGivesBackItsLimiterSlotOnceTheRaceEnds(t *testing.T) {
	live := newHarness(t)
	live.inference.chunks = []string{`data: {"id":"r","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"a"}}]}` + "\n\n", `data: {"id":"r","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"b"}}]}` + "\n\n"}
	client, handled := serveToStalledClient(t, live, streamChatBody)

	assertStalledWriteExpiredAndSlotReturned(t, live, client, handled)
}

// Test flow:
//  1. Serve a non-streamed chat completion whose race reports finished only when the test says so, to a client whose first write blocks until released and then fails only if a write deadline was armed.
//  2. Let the race report it finished and wait for the reply to reach the client, then release the write.
//  3. Assert the write failed on its armed deadline and the handler returned.
//  4. Assert the handler gave its limiter slot back and holds no buffered reply bytes.
func TestAClientThatStopsReadingAFinishedReplyGivesBackItsLimiterSlot(t *testing.T) {
	live := newHarness(t)
	live.inference.finishes = make(chan func(), 1)
	live.server.buffers = NewBufferBudget(1 << 30)
	client, handled := serveToStalledClient(t, live, `{"model":"qwen","messages":[{"role":"user","content":"hi"}]}`)

	awaitValue(t, live.inference.finishes, "the race starting")()
	assertStalledWriteExpiredAndSlotReturned(t, live, client, handled)
	if held := live.server.buffers.Held(); held != 0 {
		t.Errorf("buffered reply bytes still held = %d, want 0", held)
	}
}

type writeEvent struct {
	isDeadline bool
	deadline   time.Time
	chunkSize  int
}

type writeDeadlineRecorder struct {
	*httptest.ResponseRecorder
	events []writeEvent
}

func (w *writeDeadlineRecorder) SetWriteDeadline(deadline time.Time) error {
	w.events = append(w.events, writeEvent{isDeadline: true, deadline: deadline})
	return nil
}

func (w *writeDeadlineRecorder) Write(chunk []byte) (int, error) {
	w.events = append(w.events, writeEvent{chunkSize: len(chunk)})
	return w.ResponseRecorder.Write(chunk)
}

// Test flow:
//  1. Write a body of two and a half `clientWriteChunkBytes` through a `deadlineWriter` over a recorder that records every write deadline and every chunk it receives as one ordered list.
//  2. Flush it.
//  3. Assert the body arrived whole and the list reads deadline, chunk, deadline, chunk, deadline, chunk, deadline, with the three chunk sizes in order and the last deadline belonging to the flush.
//  4. Assert every deadline sits at least `clientWriteTimeout` past when the write began.
func TestEveryChunkWrittenToTheClientAndEveryFlushGetsAFreshDeadline(t *testing.T) {
	underlying := &writeDeadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
	writer := newDeadlineWriter(underlying, config.Defaults().Limits.MinClientReadBytesPerSecond, time.Now)
	body := strings.Repeat("a", clientWriteChunkBytes*5/2)
	writeStarted := time.Now()

	written, err := writer.Write([]byte(body))
	if err != nil || written != len(body) {
		t.Fatalf("Write() = %d, %v; want %d, nil", written, err, len(body))
	}
	writer.Flush()

	if underlying.Body.String() != body {
		t.Fatalf("the client received %d bytes, want the %d written", underlying.Body.Len(), len(body))
	}
	wantChunks := []int{clientWriteChunkBytes, clientWriteChunkBytes, clientWriteChunkBytes / 2}
	if len(underlying.events) != 2*len(wantChunks)+1 {
		t.Fatalf("events = %d, want a deadline before each of %d chunks and one for the flush", len(underlying.events), len(wantChunks))
	}
	for index, event := range underlying.events {
		if wantDeadline := index%2 == 0; event.isDeadline != wantDeadline {
			t.Fatalf("event %d is a deadline = %t, want %t: %+v", index, event.isDeadline, wantDeadline, underlying.events)
		}
		if event.isDeadline {
			if event.deadline.Before(writeStarted.Add(clientWriteTimeout)) {
				t.Errorf("deadline %d was %v, want at least %v past the moment the write began", index, event.deadline, clientWriteTimeout)
			}
		} else if want := wantChunks[index/2]; event.chunkSize != want {
			t.Errorf("chunk %d was %d bytes, want %d", index/2, event.chunkSize, want)
		}
	}
}

// Test flow:
//  1. Serve a request through `boundClientWrites` to a handler that sets only a status and writes nothing, over a recorder that records write deadlines.
//  2. Assert one write deadline was armed, at least `clientWriteTimeout` past when the request began.
func TestAHeaderOnlyReplyGoesOutWithAWriteDeadline(t *testing.T) {
	underlying := &writeDeadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
	defaults := config.Defaults()
	handler := (&Server{config: config.NewHolder(&defaults)}).boundClientWrites(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	requestStarted := time.Now()

	handler.ServeHTTP(underlying, httptest.NewRequest(http.MethodGet, "/", nil))

	if len(underlying.events) != 1 || !underlying.events[0].isDeadline {
		t.Fatalf("events = %+v, want exactly one write deadline", underlying.events)
	}
	if deadline := underlying.events[0].deadline; deadline.Before(requestStarted.Add(clientWriteTimeout)) {
		t.Errorf("deadline was %v, want at least %v past the moment the request began", deadline, clientWriteTimeout)
	}
}

const testMinReadRate = 64 << 10

type pacedClient struct {
	*httptest.ResponseRecorder
	now       time.Time
	perChunk  func(index int) time.Duration
	chunks    int
	deadline  time.Time
	deadlines []time.Duration
}

func (c *pacedClient) clock() time.Time { return c.now }

func (c *pacedClient) SetWriteDeadline(deadline time.Time) error {
	c.deadline = deadline
	c.deadlines = append(c.deadlines, deadline.Sub(c.now))
	return nil
}

func (c *pacedClient) Write(chunk []byte) (int, error) {
	finished := c.now.Add(c.perChunk(c.chunks))
	if finished.After(c.deadline) {
		c.now = maxTime(c.now, c.deadline)
		return 0, os.ErrDeadlineExceeded
	}
	c.now = finished
	c.chunks++
	return c.ResponseRecorder.Write(chunk)
}

func maxTime(first, second time.Time) time.Time {
	if first.After(second) {
		return first
	}
	return second
}

func newPacedWriter(perChunk func(index int) time.Duration) (*pacedClient, *deadlineWriter) {
	client := &pacedClient{ResponseRecorder: httptest.NewRecorder(), now: time.Unix(1_700_000_000, 0), perChunk: perChunk}
	return client, newDeadlineWriter(client, testMinReadRate, client.clock)
}

// Test flow:
//  1. Write a 2 MiB reply through a `deadlineWriter` with a minimum read rate of 64 KiB/s, to a client on a fake clock that takes 29 seconds over every 64 KiB piece, each one inside the 30-second per-write deadline.
//  2. Assert the write fails on its deadline before the whole reply is through.
//  3. Assert the client was blocked no longer than `clientWriteTimeout` plus what the bytes it took earn at the minimum rate.
func TestAClientReadingBelowTheMinimumRateIsCutOffWithinItsAllowance(t *testing.T) {
	client, writer := newPacedWriter(func(int) time.Duration { return 29 * time.Second })
	started := client.now
	body := []byte(strings.Repeat("a", 32*clientWriteChunkBytes))

	written, err := writer.Write(body)

	if !errors.Is(err, os.ErrDeadlineExceeded) || written >= len(body) {
		t.Fatalf("Write() = %d, %v; want fewer than %d bytes and %v", written, err, len(body), os.ErrDeadlineExceeded)
	}
	allowance := clientWriteTimeout + time.Duration(written)*time.Second/testMinReadRate
	if blocked := client.now.Sub(started); blocked > allowance {
		t.Fatalf("the client held the reply for %v, want at most %v for the %d bytes it took", blocked, allowance, written)
	}
}

// Test flow:
//  1. Write a 2 MiB reply through a `deadlineWriter` with a minimum read rate of 64 KiB/s, to a client on a fake clock that takes one second over every 64 KiB piece, exactly the minimum rate, and pauses 29 seconds over the tenth.
//  2. Assert the whole reply went through.
func TestAClientReadingAtTheMinimumRateGetsTheWholeReplyThroughAPause(t *testing.T) {
	client, writer := newPacedWriter(func(index int) time.Duration {
		if index == 9 {
			return 29 * time.Second
		}
		return time.Second
	})
	body := []byte(strings.Repeat("a", 32*clientWriteChunkBytes))

	written, err := writer.Write(body)

	if err != nil || written != len(body) || client.Body.Len() != len(body) {
		t.Fatalf("Write() = %d, %v with %d bytes received; want all %d and nil", written, err, client.Body.Len(), len(body))
	}
}

// Test flow:
//  1. Write ten small chunks through a `deadlineWriter` with a minimum read rate of 64 KiB/s, to a client on a fake clock that takes each one at once, moving the clock on by a minute between writes as a host slow to produce tokens does.
//  2. Assert every write armed the full `clientWriteTimeout`: time between writes is the host's, not the client's.
func TestTimeBetweenWritesIsNotChargedToTheClient(t *testing.T) {
	client, writer := newPacedWriter(func(int) time.Duration { return 0 })

	for range 10 {
		if _, err := writer.Write([]byte("data: {}\n\n")); err != nil {
			t.Fatalf("Write() error = %v, want nil", err)
		}
		client.now = client.now.Add(time.Minute)
	}

	for index, armed := range client.deadlines {
		if armed != clientWriteTimeout {
			t.Fatalf("write %d armed a deadline %v away, want %v", index, armed, clientWriteTimeout)
		}
	}
}
