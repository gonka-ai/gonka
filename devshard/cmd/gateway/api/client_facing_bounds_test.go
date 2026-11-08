package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type chunkGatedClient struct {
	*httptest.ResponseRecorder
	gatedWrite     int64
	writes         atomic.Int64
	deadlinesArmed atomic.Int64
	entered        chan struct{}
	release        chan struct{}
	releaseOnce    sync.Once
}

func newChunkGatedClient(gatedWrite int64) *chunkGatedClient {
	return &chunkGatedClient{ResponseRecorder: httptest.NewRecorder(), gatedWrite: gatedWrite, entered: make(chan struct{}, 1), release: make(chan struct{})}
}

func (c *chunkGatedClient) SetWriteDeadline(deadline time.Time) error {
	if !deadline.IsZero() {
		c.deadlinesArmed.Add(1)
	}
	return nil
}

func (c *chunkGatedClient) Write(chunk []byte) (int, error) {
	if c.writes.Add(1) == c.gatedWrite {
		c.entered <- struct{}{}
		<-c.release
	}
	return c.ResponseRecorder.Write(chunk)
}

func (c *chunkGatedClient) releaseWrites() { c.releaseOnce.Do(func() { close(c.release) }) }

func nonStreamedReplyOf(contentBytes int) string {
	return `{"id":"resp","object":"chat.completion","model":"qwen","choices":[{"index":0,"message":{"role":"assistant","content":"` + strings.Repeat("a", contentBytes) + `"},"finish_reason":"stop"}]}`
}

// Test flow:
//  1. Serve a non-streamed chat completion whose reply spans three `clientWriteChunkBytes` chunks, to a client that takes the first chunk and holds the second until released.
//  2. Wait for the second chunk to reach the client.
//  3. Assert the limiter slot is still taken, the whole reply is still charged to the buffer budget, and each chunk offered so far was given its own write deadline on top of the one armed at entry.
//  4. Release the client and assert the reply arrives whole, the slot is returned and nothing stays charged.
func TestAClientTakingAChunkAtATimeKeepsTheSlotAndTheBufferedReplyWithAFreshDeadlinePerChunk(t *testing.T) {
	live := newHarness(t)
	live.inference.reply = nonStreamedReplyOf(clientWriteChunkBytes * 5 / 2)
	live.server.buffers = NewBufferBudget(1 << 30)
	client := newChunkGatedClient(2)
	t.Cleanup(client.releaseWrites)
	handled := make(chan struct{})
	go func() {
		defer close(handled)
		live.requestInto(t, client, http.MethodPost, "/v1/chat/completions", `{"model":"qwen","messages":[{"role":"user","content":"hi"}]}`, nil)
	}()

	awaitValue(t, client.entered, "the second chunk reaching the client")
	if released := live.limiter.releases.Load(); live.limiter.acquires.Load() != 1 || released != 0 {
		t.Errorf("limiter acquires/releases while the client holds a chunk = %d/%d, want 1/0", live.limiter.acquires.Load(), released)
	}
	if held := live.server.buffers.Held(); held < int64(clientWriteChunkBytes*5/2) {
		t.Errorf("buffered reply bytes held while the client holds a chunk = %d, want at least %d", held, clientWriteChunkBytes*5/2)
	}
	if armed := client.deadlinesArmed.Load(); armed != 3 {
		t.Errorf("write deadlines armed by the second chunk = %d, want 3: one at entry and one per chunk offered", armed)
	}

	client.releaseWrites()
	awaitValue(t, handled, "the handler returning")
	if client.Body.Len() < clientWriteChunkBytes*5/2 {
		t.Errorf("the client received %d bytes, want the whole reply", client.Body.Len())
	}
	if acquired, released := live.limiter.acquires.Load(), live.limiter.releases.Load(); acquired != 1 || released != 1 {
		t.Errorf("limiter acquires/releases = %d/%d, want 1/1", acquired, released)
	}
	if held := live.server.buffers.Held(); held != 0 {
		t.Errorf("buffered reply bytes still held = %d, want 0", held)
	}
}

// Test flow:
//  1. Send a request for `/v1//models`, a path the mux cleans, through the gateway's handler over a recorder that records write deadlines.
//  2. Assert it is answered by the mux's own redirect with a write deadline armed before it went out.
func TestARedirectTheMuxAnswersItselfGoesOutWithAWriteDeadline(t *testing.T) {
	live := newHarness(t)
	redirected := &writeDeadlineRecorder{ResponseRecorder: httptest.NewRecorder()}

	live.requestInto(t, redirected, http.MethodGet, "/v1//models", "", nil)

	if redirected.Code != http.StatusTemporaryRedirect {
		t.Fatalf("status for /v1//models = %d, want the mux's %d", redirected.Code, http.StatusTemporaryRedirect)
	}
	if len(redirected.events) == 0 || !redirected.events[0].isDeadline {
		t.Errorf("the mux's redirect wrote %+v, want a write deadline armed before anything was written", redirected.events)
	}
}
