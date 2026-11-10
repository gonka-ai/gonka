package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"devshard/cmd/gateway/config"
	"devshard/cmd/gateway/metrics"
)

const concurrentCallers = 8

func liveWithRealTelemetry(t *testing.T, tune ...func(*config.Config)) *harness {
	t.Helper()
	live := newHarness(t, tune...)
	live.server.telemetry = metrics.New()
	live.server.handler = live.server.buildHandler()
	return live
}

func serveConcurrently(t *testing.T, live *harness, ctx context.Context, method, target string, bodies func(index int) io.Reader, headers map[string]string) ([]*httptest.ResponseRecorder, <-chan struct{}) {
	t.Helper()
	recorders := make([]*httptest.ResponseRecorder, concurrentCallers)
	var group sync.WaitGroup
	for index := range concurrentCallers {
		recorders[index] = httptest.NewRecorder()
		request := httptest.NewRequestWithContext(ctx, method, target, bodies(index))
		for name, value := range headers {
			request.Header.Set(name, value)
		}
		group.Go(func() { live.server.Handler().ServeHTTP(recorders[index], request) })
	}
	handled := make(chan struct{})
	go func() {
		group.Wait()
		close(handled)
	}()
	return recorders, handled
}

type heldBody struct {
	remaining  *strings.Reader
	consumed   atomic.Int64
	reachedEnd chan<- struct{}
	release    <-chan struct{}
	signalOnce sync.Once
}

func newHeldBody(body string, reachedEnd chan<- struct{}, release <-chan struct{}) *heldBody {
	return &heldBody{remaining: strings.NewReader(body), reachedEnd: reachedEnd, release: release}
}

func (b *heldBody) Read(buffer []byte) (int, error) {
	if b.remaining.Len() > 0 {
		read, _ := b.remaining.Read(buffer)
		b.consumed.Add(int64(read))
		return read, nil
	}
	b.signalOnce.Do(func() { b.reachedEnd <- struct{}{} })
	<-b.release
	return 0, io.EOF
}

func chatBodyOfBytes(model string, bodyBytes int) string {
	envelope := `{"model":"` + model + `","messages":[{"role":"user","content":""}]}`
	return `{"model":"` + model + `","messages":[{"role":"user","content":"` + strings.Repeat("a", bodyBytes-len(envelope)) + `"}]}`
}

func releasedChannel() chan struct{} {
	released := make(chan struct{})
	close(released)
	return released
}

// Test flow:
//  1. Leave qwen at the default access, admin only, and prepare `concurrentCallers` chat bodies of 1 MiB each.
//  2. Send them at once with no credential.
//  3. Assert every request was refused for its credential with not one byte of its body read and no limiter slot taken.
func TestCredentialLessBodiesAreRefusedUnreadWhenNoModelIsOpen(t *testing.T) {
	live := liveWithRealTelemetry(t, func(c *config.Config) { c.Limits.ModelAccess = map[string]string{} })
	body := chatBodyOfBytes("qwen", 1<<20)
	bodies := make([]*heldBody, concurrentCallers)
	for index := range bodies {
		bodies[index] = newHeldBody(body, make(chan struct{}, 1), releasedChannel())
	}

	recorders, handled := serveConcurrently(t, live, context.Background(), http.MethodPost, "/v1/chat/completions", func(index int) io.Reader { return bodies[index] }, nil)
	awaitValue(t, handled, "every request returning")

	for index, recorder := range recorders {
		if recorder.Code != http.StatusUnauthorized {
			t.Errorf("request %d status = %d (%s), want 401 for its missing credential", index+1, recorder.Code, recorder.Body.String())
		}
		if consumed := bodies[index].consumed.Load(); consumed != 0 {
			t.Errorf("request %d had %d body bytes read, want 0 before the refusal", index+1, consumed)
		}
	}
	if acquired := live.limiter.acquires.Load(); acquired != 0 {
		t.Errorf("limiter acquires = %d, want 0", acquired)
	}
}

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
//  1. For each pairing of a credential with the access configured for qwen, the only model, send one chat body.
//  2. Assert a caller whose credential reaches no model is refused 401 with not one byte of its body read.
//  3. Assert every other caller has its body read in full.
func TestOnlyACallerWhoseCredentialReachesNoModelIsRefusedBeforeItsBodyIsRead(t *testing.T) {
	cases := []struct {
		name    string
		access  map[string]string
		headers map[string]string
		refused bool
	}{
		{name: "no credential, qwen admin only", access: map[string]string{}, refused: true},
		{name: "no credential, qwen open", access: map[string]string{"qwen": config.ModelAccessOpen}},
		{name: "API key, qwen admin only", access: map[string]string{}, headers: callerHeaders(clientKey), refused: true},
		{name: "API key, qwen behind an API key", access: map[string]string{"qwen": config.ModelAccessAPIKey}, headers: callerHeaders(clientKey)},
		{name: "admin key, qwen admin only", access: map[string]string{}, headers: callerHeaders(adminKey)},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			live := newHarness(t, func(c *config.Config) { c.Limits.ModelAccess = testCase.access })
			body := newHeldBody(chatBody, make(chan struct{}, 1), releasedChannel())
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", body)
			for name, value := range testCase.headers {
				request.Header.Set(name, value)
			}

			live.server.Handler().ServeHTTP(recorder, request)

			consumed := body.consumed.Load()
			if testCase.refused {
				if recorder.Code != http.StatusUnauthorized || consumed != 0 {
					t.Errorf("status = %d with %d body bytes read, want 401 with 0", recorder.Code, consumed)
				}
				return
			}
			if consumed != int64(len(chatBody)) {
				t.Errorf("body bytes read = %d, want all %d (status %d: %s)", consumed, len(chatBody), recorder.Code, recorder.Body.String())
			}
		})
	}
}

// Test flow:
//  1. Give the gateway a body budget of one and a half chat bodies, and send one body that holds its end until released.
//  2. Once it is read in full, send a second body of the same size.
//  3. Assert the second is refused 503 with a Retry-After before it was read in full, while the first holds its bytes.
//  4. Release the first and assert it is served, nothing stays reserved, and a third body is read and served.
func TestABodyThatWouldOverrunTheBodyBudgetIsRefusedBeforeItIsReadInFull(t *testing.T) {
	live := newHarness(t)
	body := chatBodyOfBytes("qwen", 1<<20)
	live.server.requestBodies = NewBufferBudget(int64(len(body)) * 3 / 2)
	reachedEnd := make(chan struct{}, 1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseFirst := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseFirst)
	first := httptest.NewRecorder()
	firstHandled := make(chan struct{})
	go func() {
		defer close(firstHandled)
		live.server.Handler().ServeHTTP(first, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", newHeldBody(body, reachedEnd, release)))
	}()
	awaitValue(t, reachedEnd, "the first body read to its end")

	second := newHeldBody(body, make(chan struct{}, 1), releasedChannel())
	refused := httptest.NewRecorder()
	live.server.Handler().ServeHTTP(refused, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", second))

	if refused.Code != http.StatusServiceUnavailable || refused.Header().Get("Retry-After") == "" {
		t.Errorf("second body = %d, Retry-After %q (%s), want 503 with a Retry-After", refused.Code, refused.Header().Get("Retry-After"), refused.Body.String())
	}
	if consumed := second.consumed.Load(); consumed >= int64(len(body)) {
		t.Errorf("second body bytes read = %d, want fewer than its %d", consumed, len(body))
	}
	if held := live.server.requestBodies.Held(); held != int64(len(body)) {
		t.Errorf("body bytes reserved while the first is held = %d, want its %d", held, len(body))
	}

	releaseFirst()
	awaitValue(t, firstHandled, "the first request returning")
	if first.Code != http.StatusOK {
		t.Errorf("first body = %d (%s), want 200", first.Code, first.Body.String())
	}
	if held := live.server.requestBodies.Held(); held != 0 {
		t.Errorf("body bytes still reserved = %d, want 0", held)
	}
	if third := live.request(t, http.MethodPost, "/v1/chat/completions", body, nil); third.Code != http.StatusOK {
		t.Errorf("third body = %d (%s), want 200 once the budget is free", third.Code, third.Body.String())
	}
}

type blockingPromptText struct {
	maximumTokens uint64
	countedTokens uint64
	inside        atomic.Int64
	entered       chan struct{}
	release       chan struct{}
	releaseOnce   sync.Once
}

func (p *blockingPromptText) Prompt(string, []byte) (PromptText, bool) { return p, true }

func (p *blockingPromptText) MaximumTokens() uint64 { return p.maximumTokens }

func (p *blockingPromptText) Count() (TokenizedPrompt, bool) {
	p.inside.Add(1)
	p.entered <- struct{}{}
	<-p.release
	return TokenizedPrompt{Tokens: p.countedTokens, MinimumTokens: p.countedTokens}, true
}

func (p *blockingPromptText) CountWithPieceCap() (TokenizedPrompt, bool) { return p.Count() }

func (p *blockingPromptText) releaseCounts() { p.releaseOnce.Do(func() { close(p.release) }) }

// Test flow:
//  1. Pin qwen's context at 20 000 tokens, give the prompt count gate one slot and take it, and wire a prompt whose byte bound fails the context and whose count waits until released.
//  2. Send `concurrentCallers` chat requests at once, then cancel their contexts while the gate stays full.
//  3. Assert every request returned without entering the count and without taking a limiter slot.
//  4. Free the slot, send one more request, and assert it enters the count and, once released, is refused for its context length with no limiter slot taken.
func TestTheContextCheckWaitsForAPromptCountSlotBeforeCounting(t *testing.T) {
	live := liveWithRealTelemetry(t, pinContextLength(20_000))
	prompt := &blockingPromptText{maximumTokens: 30_000, countedTokens: 30_000, entered: make(chan struct{}, concurrentCallers+1), release: make(chan struct{})}
	live.server.promptTokens = prompt
	live.server.promptCounts = newPromptCountGate(1)
	t.Cleanup(prompt.releaseCounts)
	if !live.server.promptCounts.tryAcquire() {
		t.Fatal("the gate refused its only slot while empty")
	}

	ctx, cancel := context.WithCancel(context.Background())
	_, handled := serveConcurrently(t, live, ctx, http.MethodPost, "/v1/chat/completions", func(int) io.Reader { return strings.NewReader(chatBody) }, nil)
	cancel()
	awaitValue(t, handled, "every request with a cancelled context returning")
	if inside := prompt.inside.Load(); inside != 0 {
		t.Errorf("counts entered while the gate was full = %d, want 0", inside)
	}
	if acquired := live.limiter.acquires.Load(); acquired != 0 {
		t.Errorf("limiter acquires while the gate was full = %d, want 0", acquired)
	}

	live.server.promptCounts.release()
	counted := make(chan *httptest.ResponseRecorder, 1)
	go func() { counted <- live.request(t, http.MethodPost, "/v1/chat/completions", chatBody, nil) }()
	awaitValue(t, prompt.entered, "a request entering the count once a slot was free")
	prompt.releaseCounts()
	recorder := awaitValue(t, counted, "the counted request returning")
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "context_length_exceeded") {
		t.Errorf("counted request = %d (%s), want 400 context_length_exceeded", recorder.Code, recorder.Body.String())
	}
	if acquired := live.limiter.acquires.Load(); acquired != 0 {
		t.Errorf("limiter acquires = %d, want 0", acquired)
	}
}

// Test flow:
//  1. Send a request for `/v1//models`, a path the mux cleans, through the gateway's handler over a recorder that records write deadlines.
//  2. Assert it is answered by the mux's own redirect, the status and Location a bare `http.ServeMux` gives the same path on this Go version, with a write deadline armed before it went out.
func TestARedirectTheMuxAnswersItselfGoesOutWithAWriteDeadline(t *testing.T) {
	live := newHarness(t)
	redirected := &writeDeadlineRecorder{ResponseRecorder: httptest.NewRecorder()}

	live.requestInto(t, redirected, http.MethodGet, "/v1//models", "", nil)

	bare := httptest.NewRecorder()
	http.NewServeMux().ServeHTTP(bare, httptest.NewRequest(http.MethodGet, "/v1//models", nil))
	if redirected.Code != bare.Code || redirected.Header().Get("Location") != bare.Header().Get("Location") {
		t.Fatalf("answer for /v1//models = %d to %q, want the bare mux's %d to %q", redirected.Code, redirected.Header().Get("Location"), bare.Code, bare.Header().Get("Location"))
	}
	if len(redirected.events) == 0 || !redirected.events[0].isDeadline {
		t.Errorf("the mux's redirect wrote %+v, want a write deadline armed before anything was written", redirected.events)
	}
}
