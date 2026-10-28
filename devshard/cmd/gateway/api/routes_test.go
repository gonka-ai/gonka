package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"devshard/cmd/gateway/config"
	"devshard/cmd/gateway/engine"
	"devshard/cmd/gateway/escrow"
	"devshard/cmd/gateway/limits"
	"devshard/cmd/gateway/registry"
	"devshard/cmd/gateway/scheduler"
	"devshard/transport"
	"devshard/types"
)

// Test flow:
//  1. POST, as the admin, a chat completion naming a model nobody serves.
//  2. Assert the response is 400 and names the model in its message.
//  3. Assert no limiter slots, token budget, or races were consumed.
//  4. Assert the response carries no Retry-After header.
func TestAnUnroutableModelIsRejectedBeforeTheLimiterAndTheRace(t *testing.T) {
	live := newHarness(t)
	recorder := live.request(t, http.MethodPost, "/v1/chat/completions",
		`{"model":"not-served","messages":[{"role":"user","content":"hi"}]}`, adminHeaders())

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d (%s), want 400", recorder.Code, recorder.Body.String())
	}
	if got := live.limiter.acquires.Load(); got != 0 {
		t.Fatalf("limiter slots taken: got %d, want 0", got)
	}
	if got := live.limiter.tokens.Load(); got != 0 {
		t.Fatalf("input-token budget consumed: got %d, want 0", got)
	}
	if got := live.inference.runs.Load(); got != 0 {
		t.Fatalf("races started: got %d, want 0", got)
	}
	if !strings.Contains(recorder.Body.String(), `unsupported model \"not-served\"`) {
		t.Fatalf("the rejection must name the model and the routable set: %s", recorder.Body.String())
	}
	if got := recorder.Header().Get("Retry-After"); got != "" {
		t.Fatalf("Retry-After: got %q on a model nobody offers, want none — that is the client's mistake, not a wait", got)
	}
}

// Test flow:
//  1. Configure `limits.model_access` to offer a model nothing routes to.
//  2. POST a chat completion for that model.
//  3. Assert the response is 503 with Retry-After set to the escrow tick interval, not the 400 an unlisted model gets.
//  4. Assert no limiter slots or races were consumed.
func TestAModelOfferedThroughAccessAnswersUnavailableWithTheEscrowTick(t *testing.T) {
	live := newHarness(t, func(configuration *config.Config) {
		configuration.Limits.ModelAccess = map[string]string{"offered-model": config.ModelAccessOpen}
	})
	recorder := live.request(t, http.MethodPost, "/v1/chat/completions",
		`{"model":"offered-model","messages":[{"role":"user","content":"hi"}]}`, nil)

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status: got %d (%s), want 503 — the operator offers this model, even with nothing routable", recorder.Code, recorder.Body.String())
	}
	want := strconv.Itoa(int(escrow.TickInterval.Seconds()))
	if got := recorder.Header().Get("Retry-After"); got != want {
		t.Fatalf("Retry-After: got %q, want %q (the escrow tick interval)", got, want)
	}
	if got := live.limiter.acquires.Load(); got != 0 {
		t.Fatalf("limiter slots taken: got %d, want 0", got)
	}
	if got := live.inference.runs.Load(); got != 0 {
		t.Fatalf("races started: got %d, want 0", got)
	}
}

// Test flow:
//  1. Configure `limits.model_limits` alone to offer a model nothing routes to.
//  2. POST, as the admin, a chat completion for that model: without an access entry it is admin-only.
//  3. Assert the response is 503 with Retry-After set to the escrow tick interval.
func TestAModelOfferedOnlyThroughModelLimitsAnswersUnavailable(t *testing.T) {
	live := newHarness(t, func(configuration *config.Config) {
		configuration.Limits.ModelLimits = map[string]config.ModelLimits{"offered-model": {}}
	})
	recorder := live.request(t, http.MethodPost, "/v1/chat/completions",
		`{"model":"offered-model","messages":[{"role":"user","content":"hi"}]}`, adminHeaders())

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status: got %d (%s), want 503 — model_limits alone still offers the model", recorder.Code, recorder.Body.String())
	}
	want := strconv.Itoa(int(escrow.TickInterval.Seconds()))
	if got := recorder.Header().Get("Retry-After"); got != want {
		t.Fatalf("Retry-After: got %q, want %q (the escrow tick interval)", got, want)
	}
}

// Test flow:
//  1. Clear `limits.model_access`, as every deployment starts, with API keys configured.
//  2. POST a chat completion for the served model without credentials, and with a client API key.
//  3. Assert each is refused 401 before any limiter slot or race: a model no entry opens is admin-only.
func TestAModelNoAccessEntryOpensIsRefusedToAnonymousAndAPIKeyCallers(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		headers map[string]string
	}{
		{"anonymous", nil},
		{"api key", map[string]string{"Authorization": "Bearer " + clientKey}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			live := newHarness(t, func(configuration *config.Config) { configuration.Limits.ModelAccess = nil })

			recorder := live.request(t, http.MethodPost, "/v1/chat/completions", chatBody, testCase.headers)

			if recorder.Code != http.StatusUnauthorized {
				t.Fatalf("status %d (%s), want 401", recorder.Code, recorder.Body.String())
			}
			if got := live.limiter.acquires.Load(); got != 0 {
				t.Fatalf("limiter slots taken: got %d, want 0", got)
			}
			if got := live.inference.runs.Load(); got != 0 {
				t.Fatalf("races started: got %d, want 0 — a refused caller must not spend an escrow", got)
			}
		})
	}
}

// Test flow:
//  1. Clear `limits.model_access`, as every deployment starts.
//  2. POST a chat completion for the served model with the admin key.
//  3. Assert it is served and races once: the admin reaches every model, listed or not.
func TestTheAdminReachesAModelNoAccessEntryOpens(t *testing.T) {
	live := newHarness(t, func(configuration *config.Config) { configuration.Limits.ModelAccess = nil })

	recorder := live.request(t, http.MethodPost, "/v1/chat/completions", chatBody, adminHeaders())

	if recorder.Code != http.StatusOK {
		t.Fatalf("status %d (%s), want 200", recorder.Code, recorder.Body.String())
	}
	if got := live.inference.runs.Load(); got != 1 {
		t.Fatalf("races started: got %d, want 1", got)
	}
}

// Test flow:
//  1. Clear the harness's registry of every model and escrow.
//  2. POST a chat completion.
//  3. Assert the response is 503 with Retry-After set to the escrow tick interval, so an empty registry reads as not ready rather than open.
//  4. Assert no limiter slots or races were consumed.
func TestAnEmptyRegistryFailsClosed(t *testing.T) {
	live := newHarness(t)
	live.escrows.models = nil
	live.escrows.escrows = nil

	recorder := live.request(t, http.MethodPost, "/v1/chat/completions", chatBody, nil)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status: got %d (%s), want 503 — an empty registry is not ready, not open", recorder.Code, recorder.Body.String())
	}
	want := strconv.Itoa(int(escrow.TickInterval.Seconds()))
	if got := recorder.Header().Get("Retry-After"); got != want {
		t.Fatalf("Retry-After: got %q, want %q (the escrow tick interval)", got, want)
	}
	if got := live.limiter.acquires.Load(); got != 0 {
		t.Fatalf("limiter slots taken on an unready gateway: got %d, want 0", got)
	}
	if got := live.inference.runs.Load(); got != 0 {
		t.Fatalf("races started on an unready gateway: got %d, want 0", got)
	}
}

// Test flow:
//  1. POST a chat completion body with no model field.
//  2. Assert the response is 400 and no limiter slot was taken.
func TestAChatRequestWithoutAModelIsAClientError(t *testing.T) {
	live := newHarness(t)
	recorder := live.request(t, http.MethodPost, "/v1/chat/completions",
		`{"messages":[{"role":"user","content":"hi"}]}`, nil)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d (%s), want 400", recorder.Code, recorder.Body.String())
	}
	if got := live.limiter.acquires.Load(); got != 0 {
		t.Fatalf("limiter slots taken: got %d, want 0", got)
	}
}

// Test flow:
//  1. Make the limiter refuse with a "too many concurrent requests" reason.
//  2. POST a chat completion.
//  3. Assert the response is 429 and no race started.
func TestTheLimiterRejectionReachesTheClientAsTooManyRequests(t *testing.T) {
	live := newHarness(t)
	live.limiter.err = &limits.RateLimitError{Reason: "too many concurrent requests"}
	recorder := live.request(t, http.MethodPost, "/v1/chat/completions", chatBody, nil)
	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("status: got %d, want 429", recorder.Code)
	}
	if got := live.inference.runs.Load(); got != 0 {
		t.Fatalf("races started after a limiter rejection: got %d", got)
	}
}

// Test flow:
//  1. Make inference fail every attempt.
//  2. POST a chat completion.
//  3. Assert the response is 502 with the limiter slot released and its input-token budget freed.
//  4. Assert the X-Request-Id header is still set on the error path.
func TestAFailedRaceIsA502AndReturnsItsSlot(t *testing.T) {
	live := newHarness(t)
	live.inference.reply = ""
	live.inference.err = errors.New("every attempt failed")
	recorder := live.request(t, http.MethodPost, "/v1/chat/completions", chatBody, nil)
	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("status: got %d, want 502", recorder.Code)
	}
	if got := live.limiter.releases.Load(); got != 1 {
		t.Fatalf("limiter releases: got %d, want 1", got)
	}
	if got := live.limiter.tokens.Load(); got != 0 {
		t.Fatalf("input-token budget left held: got %d, want 0", got)
	}
	if got := recorder.Header().Get("X-Request-Id"); got != "request-1" {
		t.Fatalf("X-Request-Id on the error path: got %q", got)
	}
}

// Test flow:
//  1. Make inference reply with a multi-event SSE stream ending in a final data event.
//  2. POST a non-streaming chat completion.
//  3. Assert the response is 200 with Content-Type application/json and the request/devshard ID headers set.
//  4. Decode the body as JSON and assert it is the assembled last event, not the raw SSE envelope.
func TestASuccessfulNonStreamingReplyIsJSONAndCarriesItsIdentifiers(t *testing.T) {
	live := newHarness(t)
	live.inference.reply = "data: {\"id\":\"first\",\"choices\":[]}\n\ndata: {\"id\":\"resp\",\"choices\":[{\"message\":{\"content\":\"hi\"}}]}\n\ndata: [DONE]\n\n"

	recorder := live.request(t, http.MethodPost, "/v1/chat/completions", chatBody, nil)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status: got %d (%s)", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type: got %q", got)
	}
	if got := recorder.Header().Get("X-Request-Id"); got != "request-1" {
		t.Fatalf("X-Request-Id: got %q", got)
	}
	if got := recorder.Header().Get("X-Devshard-ID"); got != "7" {
		t.Fatalf("X-Devshard-ID: got %q", got)
	}
	var assembled map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &assembled); err != nil {
		t.Fatalf("the body labelled application/json does not parse as JSON: %v (%s)", err, recorder.Body.String())
	}
	if assembled["id"] != "resp" {
		t.Fatalf("assembled body: got %s, want the last data event of the stream", recorder.Body.String())
	}
}

// Test flow:
//  1. Make inference reply with a stream carrying logprobs and prompt_token_ids fields.
//  2. POST a non-streaming chat completion.
//  3. Assert the assembled body drops both internal fields while keeping the actual content.
func TestANonStreamingReplyIsStrippedOfItsInternalFields(t *testing.T) {
	live := newHarness(t)
	live.inference.reply = "data: {\"id\":\"resp\",\"choices\":[{\"logprobs\":{\"content\":[]},\"message\":{\"content\":\"hi\"}}],\"prompt_token_ids\":[1,2]}\n\ndata: [DONE]\n\n"

	recorder := live.request(t, http.MethodPost, "/v1/chat/completions", chatBody, nil)

	body := recorder.Body.String()
	for _, stripped := range []string{"logprobs", "prompt_token_ids"} {
		if strings.Contains(body, stripped) {
			t.Fatalf("%s reached the client: %s", stripped, body)
		}
	}
	if !strings.Contains(body, `"content":"hi"`) {
		t.Fatalf("the strip took the reply with it: %s", body)
	}
}

// Test flow:
//  1. Define a table of streamed outcomes, varying across a host that never terminated, a host that terminated itself, a failure after the client saw bytes, and a winner that produced nothing.
//  2. For each case, POST a streaming chat completion with that outcome.
//  3. Assert the body ends with the case's expected tail.
//  4. Where specified, assert the terminator appears exactly once.
func TestAStreamCarriesTheTerminatorOnEveryExitButATruncatedAnswer(t *testing.T) {
	testCases := []struct {
		name     string
		chunks   []string
		failure  error
		want     string
		wantOnce string
	}{
		{
			name:   "a host that never terminated",
			chunks: []string{"data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n"},
			want:   "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\ndata: [DONE]\n\n",
		},
		{
			name:     "a host that terminated itself",
			chunks:   []string{"data: {\"choices\":[]}\n\n", "data: [DONE]\n\n"},
			want:     "data: {\"choices\":[]}\n\ndata: [DONE]\n\n",
			wantOnce: "data: [DONE]\n\n",
		},
		{
			name:    "a failure after the client saw bytes",
			chunks:  []string{"data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n"},
			failure: errors.New("winner failed after streaming started"),
			want:    "data: {\"error\":{\"message\":\"winner failed after streaming started\"}}\n\ndata: [DONE]\n\n",
		},
		{
			name: "a winner that produced nothing",
			want: "data: [DONE]\n\n",
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			live := newHarness(t)
			live.inference.reply = ""
			live.inference.chunks = testCase.chunks
			live.inference.err = testCase.failure

			recorder := live.request(t, http.MethodPost, "/v1/chat/completions", streamChatBody, nil)

			body := recorder.Body.String()
			if !strings.HasSuffix(body, testCase.want) {
				t.Fatalf("streamed body: got %q, want it to end with %q", body, testCase.want)
			}
			if testCase.wantOnce != "" && strings.Count(body, testCase.wantOnce) != 1 {
				t.Fatalf("streamed body: got %q, want exactly one %q", body, testCase.wantOnce)
			}
		})
	}
}

// Test flow:
//  1. Make the engine write one content chunk and then fail with `engine.ErrAnswerTruncated`.
//  2. POST the chat once buffered and once streaming.
//  3. Assert the buffered reply is 502 with the error's message and none of the partial content.
//  4. Assert the stream keeps the content, ends with the error event, and carries no [DONE].
func TestATruncatedAnswerReachesTheClientAsAnError(t *testing.T) {
	const partial = "data: {\"choices\":[{\"delta\":{\"content\":\"half\"}}]}\n\n"

	buffered := newHarness(t)
	buffered.inference.reply = ""
	buffered.inference.chunks = []string{partial}
	buffered.inference.err = engine.ErrAnswerTruncated
	recorder := buffered.request(t, http.MethodPost, "/v1/chat/completions", chatBody, nil)
	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("buffered status = %d, want %d (%s)", recorder.Code, http.StatusBadGateway, recorder.Body)
	}
	if body := recorder.Body.String(); !strings.Contains(body, engine.ErrAnswerTruncated.Error()) || strings.Contains(body, "half") {
		t.Fatalf("buffered body = %s, want the error and no partial content", body)
	}

	streamed := newHarness(t)
	streamed.inference.reply = ""
	streamed.inference.chunks = []string{partial}
	streamed.inference.err = engine.ErrAnswerTruncated
	recorder = streamed.request(t, http.MethodPost, "/v1/chat/completions", streamChatBody, nil)
	body := recorder.Body.String()
	wantTail := partial + "data: {\"error\":{\"message\":\"" + engine.ErrAnswerTruncated.Error() + "\"}}\n\n"
	if !strings.HasSuffix(body, wantTail) {
		t.Fatalf("streamed body = %q, want it to end with %q", body, wantTail)
	}
	if strings.Contains(body, "[DONE]") {
		t.Fatalf("streamed body = %q, want no terminator on a truncated answer", body)
	}
}

// Test flow:
//  1. Make inference reply with one SSE chunk.
//  2. POST a streaming chat completion.
//  3. Assert the response is 200 with Content-Type text/event-stream, the request ID header set, and the recorder flushed.
//  4. Assert the body matches the chunk followed by the terminator.
func TestAStreamedReplyIsFlushedAsEventStream(t *testing.T) {
	live := newHarness(t)
	live.inference.reply = "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n"
	recorder := live.request(t, http.MethodPost, "/v1/chat/completions",
		`{"model":"qwen","stream":true,"messages":[{"role":"user","content":"hi"}]}`, nil)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status: got %d (%s)", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("Content-Type: got %q", got)
	}
	if got := recorder.Header().Get("X-Request-Id"); got != "request-1" {
		t.Fatalf("X-Request-Id must be set before the first byte: got %q", got)
	}
	if !recorder.Flushed {
		t.Fatal("the stream was never flushed; a buffered SSE response reaches the client as nothing")
	}
	want := "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\ndata: [DONE]\n\n"
	if got := recorder.Body.String(); got != want {
		t.Fatalf("body: got %q, want %q", got, want)
	}
}

// Test flow:
//  1. Make inference emit one chunk and then fail.
//  2. POST a streaming chat completion.
//  3. Assert the response still reports the 200 the client already saw.
//  4. Assert the failure is named in the body instead.
func TestAFailureAfterTheFirstByteKeepsTheStatusTheClientAlreadySaw(t *testing.T) {
	live := newHarness(t)
	live.inference.reply = "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n"
	live.inference.err = errors.New("winner failed after streaming started")
	recorder := live.request(t, http.MethodPost, "/v1/chat/completions",
		`{"model":"qwen","stream":true,"messages":[{"role":"user","content":"hi"}]}`, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status: got %d, want the 200 the client already received", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), "winner failed after streaming started") {
		t.Fatalf("body: got %q, want the failure named where the status no longer can", recorder.Body.String())
	}
}

// Test flow:
//  1. Register two routable models.
//  2. GET /v1/models.
//  3. Assert the decoded list carries both model IDs in order with a created timestamp from the injected clock.
func TestTheModelListNamesEveryRoutableModel(t *testing.T) {
	live := newHarness(t)
	live.escrows.models = []string{"qwen", "kimi"}
	recorder := live.request(t, http.MethodGet, "/v1/models", "", nil)

	var listed modelListResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decoding: %v (%s)", err, recorder.Body.String())
	}
	if listed.Object != "list" || len(listed.Data) != 2 {
		t.Fatalf("got %+v", listed)
	}
	if listed.Data[0].ID != "qwen" || listed.Data[1].ID != "kimi" {
		t.Fatalf("model ids: got %q and %q", listed.Data[0].ID, listed.Data[1].ID)
	}
	if listed.Data[0].Created != 1700000000 {
		t.Fatalf("created must come from the injected clock: got %d", listed.Data[0].Created)
	}
}

// Test flow:
//  1. Register two models but scope one escrow to only one of them.
//  2. GET that escrow's pinned models route.
//  3. Assert the decoded list carries only that escrow's model.
func TestThePerEscrowModelListIsScopedToThatEscrow(t *testing.T) {
	live := newHarness(t)
	live.escrows.models = []string{"qwen", "kimi"}
	live.escrows.escrows = append(live.escrows.escrows, scheduler.Escrow{ID: "8", Model: "kimi"})
	recorder := live.request(t, http.MethodGet, "/devshard/8/v1/models", "", nil)

	var listed modelListResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if len(listed.Data) != 1 || listed.Data[0].ID != "kimi" {
		t.Fatalf("got %+v", listed.Data)
	}
}

// Test flow:
//  1. Build a server whose registry routes no escrow 8.
//  2. Send a chat pinned to escrow 8.
//  3. Assert the answer names an unknown devshard.
func TestAPinnedChatToAnEscrowNotRoutedIsUnknown(t *testing.T) {
	live := newHarness(t)

	pinned := live.request(t, http.MethodPost, "/devshard/8/v1/chat/completions", chatBody, nil)

	if pinned.Code != http.StatusNotFound {
		t.Errorf("pin to an escrow not routed: got %d (%s), want 404", pinned.Code, pinned.Body.String())
	}
	if got := live.inference.runs.Load(); got != 0 {
		t.Errorf("races started: got %d, want 0", got)
	}
}

// Test flow:
//  1. Set the chain snapshot's blocked flag while configuring relaxed PoC mode.
//  2. GET /v1/status.
//  3. Assert the decoded status reports not blocked, since relaxed mode is serving traffic, and lists the registered devshard.
func TestStatusReportsTheAdmissionDecisionNotTheRawChainFlag(t *testing.T) {
	live := newHarness(t)
	live.snapshots.snapshot.RequestsBlocked = true
	live.swapConfig(func(next *config.Config) { next.Modes.PoCMode = config.PoCModeRelaxed })

	recorder := live.request(t, http.MethodGet, "/v1/status", "", nil)
	var reported statusResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &reported); err != nil {
		t.Fatalf("decoding: %v (%s)", err, recorder.Body.String())
	}
	if reported.RequestsBlocked {
		t.Fatal("status reported blocked while relaxed mode was serving traffic")
	}
	if len(reported.Devshards) != 1 || reported.Devshards[0].EscrowID != "7" {
		t.Fatalf("devshards: got %+v", reported.Devshards)
	}
}

// Test flow:
//  1. Define a table of request bodies, varying across empty, one byte, four bytes, and five bytes.
//  2. For each case, count it the way a request without a tokenizer is counted.
//  3. Assert the result matches the expected token count, with a floor of one even for an empty body.
func TestTheByteEstimateHasAFloorOfOne(t *testing.T) {
	testCases := []struct {
		name string
		body string
		want uint64
	}{
		{name: "empty", body: "", want: 1},
		{name: "one byte", body: "a", want: 1},
		{name: "four bytes", body: "abcd", want: 1},
		{name: "five bytes", body: "abcde", want: 2},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := (&Server{}).countPrompt("qwen", requestPrompt{bodyBytes: len(testCase.body)}).tokens; got != testCase.want {
				t.Fatalf("got %d, want %d", got, testCase.want)
			}
		})
	}
}

// Test flow:
//  1. Make inference stream two chunks over a real HTTP server wrapping the harness handler.
//  2. POST a streaming chat completion over the wire, not through the recorder.
//  3. Assert the response is 200.
//  4. Assert the X-Devshard-ID header read from the live response carries the serving escrow's ID, since reading from a recorder instead could pass even on a broken stream.
func TestALiveStreamCarriesTheEscrowHeader(t *testing.T) {
	live := newHarness(t)
	live.inference.chunks = []string{
		"data: {\"choices\":[{\"delta\":{\"content\":\"one\"}}]}\n\n",
		"data: [DONE]\n\n",
	}
	upstream := httptest.NewServer(live.server.Handler())
	t.Cleanup(upstream.Close)

	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, upstream.URL+"/v1/chat/completions", strings.NewReader(string(streamChatBody)))
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	request.Header.Set("Authorization", "Bearer "+clientKey)
	response, err := upstream.Client().Do(request)
	if err != nil {
		t.Fatalf("sending request: %v", err)
	}
	t.Cleanup(func() { _ = response.Body.Close() })

	if response.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d", response.StatusCode)
	}
	if got := response.Header.Get("X-Devshard-ID"); got != "7" {
		t.Fatalf("X-Devshard-ID on a live stream: got %q, want the escrow that served it", got)
	}
}

// Test flow:
//  1. Register a live session for an escrow.
//  2. GET /v1/status.
//  3. Assert the body carries the session's effective state-root-and-protocol version.
func TestTheStatusReportsTheSessionVersion(t *testing.T) {
	live := newHarness(t)
	session, machine := newLiveSession(t)
	live.escrows.escrows = []scheduler.Escrow{{ID: liveEscrowID, Model: "qwen"}}
	live.escrows.sessions[liveEscrowID] = registry.NewSessionHandle(session, machine)

	recorder := live.request(t, http.MethodGet, "/v1/status", "", nil)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status: got %d", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), `"session_version":"`+types.EffectiveStateRootAndProtocolVersion+`"`) {
		t.Fatalf("the status carries no session version: %s", recorder.Body.String())
	}
}

// Test flow:
//  1. Register an escrow session that counts full state copies, with balance 900 and protocol version v5.
//  2. Request the escrow's status.
//  3. Assert the status carries the balance and the protocol version.
//  4. Assert the session was never asked for a full copy of its state.
func TestDevshardStatusReadsBalanceAndVersionWithoutCopyingTheState(t *testing.T) {
	live := newHarness(t)
	session := &pointReadSession{balance: 900, version: "v5"}
	live.escrows.sessions["7"] = session

	recorder := live.request(t, http.MethodGet, "/devshard/7/v1/status", "", nil)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d: %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	var answer struct {
		Devshards []devshardStatus `json:"devshards"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &answer); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if len(answer.Devshards) != 1 || answer.Devshards[0].Balance != 900 || answer.Devshards[0].SessionVersion != "v5" {
		t.Errorf("devshards = %+v, want one escrow with balance 900 and session_version v5", answer.Devshards)
	}
	if copies := session.snapshots.Load(); copies != 0 {
		t.Errorf("SnapshotState called %d times, want 0", copies)
	}
}

// Test flow:
//  1. Make the limiter refuse with a "too many concurrent requests" reason.
//  2. POST a chat completion.
//  3. Assert exactly one rejection was recorded with reason `concurrent_requests` and the model that was turned away.
func TestALimiterRejectionNamesTheCapItHit(t *testing.T) {
	live := newHarness(t)
	live.limiter.err = &limits.RateLimitError{Reason: "too many concurrent requests"}

	live.request(t, http.MethodPost, "/v1/chat/completions", chatBody, nil)

	reasons := live.rejections.reasons()
	if len(reasons) != 1 || reasons[0] != "concurrent_requests" {
		t.Fatalf("recorded %v, want the cap that rejected the request", reasons)
	}
	if live.rejections.model != "qwen" {
		t.Errorf("model = %q, want the model that was turned away", live.rejections.model)
	}
}

// Test flow:
//  1. Build a chat completion whose content alone fills the host budget left beside the catch-up reserve, but still under the raw ingest cap.
//  2. POST it.
//  3. Assert the response is 413 and no limiter slot was taken, so the refusal precedes admission.
func TestChatRefusesABodyNoHostCouldBeSent(t *testing.T) {
	live := newHarness(t)
	oversized := fmt.Sprintf(`{"model":"qwen","messages":[{"role":"user","content":%q}]}`,
		strings.Repeat("x", transport.MaxHostRequestBytes-hostCatchUpReserveBytes))

	recorder := live.request(t, http.MethodPost, "/v1/chat/completions", oversized, nil)

	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status: got %d (%s), want 413", recorder.Code, recorder.Body.String())
	}
	if got := live.limiter.acquires.Load(); got != 0 {
		t.Fatalf("limiter slots taken: got %d, want 0 — the refusal must precede admission", got)
	}
}

// Test flow:
//  1. Build an 8 MiB chat completion, which fits a host's limit as the raw envelope bytes a prompt travels as.
//  2. POST it.
//  3. Assert the response is not refused as too large.
func TestChatServesABodyThatStillFitsOnceEncoded(t *testing.T) {
	live := newHarness(t)
	large := fmt.Sprintf(`{"model":"qwen","messages":[{"role":"user","content":%q}]}`,
		strings.Repeat("x", 8<<20))

	recorder := live.request(t, http.MethodPost, "/v1/chat/completions", large, nil)

	if recorder.Code == http.StatusRequestEntityTooLarge {
		t.Fatalf("a body that fits once encoded was refused as too large: %s", recorder.Body.String())
	}
}
