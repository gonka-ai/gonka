package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"devshard/cmd/gateway/config"
)

// Test flow:
//  1. Start a real HTTP server over the harness handler.
//  2. PUT an admin settings body one byte past `adminIngestLimit`.
//  3. Assert the response is 413 and the connection is closed rather than kept alive.
//  4. Assert the oversized body reached no operations.
func TestAnOversizedAdminBodyIsRejectedAndTheConnectionIsClosed(t *testing.T) {
	live := newHarness(t)
	upstream := httptest.NewServer(live.server.Handler())
	t.Cleanup(upstream.Close)

	oversized := `{"disabled_message":"` + strings.Repeat("A", adminIngestLimit+1) + `"}`
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPut, upstream.URL+"/v1/admin/settings", strings.NewReader(oversized))
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	request.Header.Set("Authorization", "Bearer "+adminKey)
	response, err := upstream.Client().Do(request)
	if err != nil {
		t.Fatalf("sending request: %v", err)
	}
	t.Cleanup(func() { _ = response.Body.Close() })

	if response.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status: got %d, want 413", response.StatusCode)
	}
	if !response.Close {
		t.Fatal("the server kept the connection open after refusing an oversized body")
	}
	if got := len(live.operations.recordedCalls()); got != 0 {
		t.Fatalf("an oversized body reached %d operations", got)
	}
}

// Test flow:
//  1. PUT an admin settings body well within the ingest limit.
//  2. Assert the response is 200.
func TestAnAdminBodyInsideTheBoundIsAccepted(t *testing.T) {
	live := newHarness(t)
	body := `{"disabled_message":"` + strings.Repeat("A", 1024) + `"}`
	recorder := live.request(t, http.MethodPut, "/v1/admin/settings", body, adminHeaders())
	if recorder.Code != http.StatusOK {
		t.Fatalf("status: got %d (%s), want 200", recorder.Code, recorder.Body.String())
	}
}

// Test flow:
//  1. POST a chat completion whose prompt fills `chatIngestLimit`.
//  2. Assert the response is 413.
//  3. Assert the oversized body took no limiter slots and started no races.
func TestAnOversizedChatBodyIsRejectedBeforeAnythingRuns(t *testing.T) {
	live := newHarness(t)
	prompt := strings.Repeat("A", chatIngestLimit)
	recorder := live.request(t, http.MethodPost, "/v1/chat/completions",
		`{"model":"qwen","messages":[{"role":"user","content":"`+prompt+`"}]}`, nil)
	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status: got %d, want 413", recorder.Code)
	}
	if got := live.limiter.acquires.Load(); got != 0 {
		t.Fatalf("an oversized body took %d limiter slots", got)
	}
	if got := live.inference.runs.Load(); got != 0 {
		t.Fatalf("an oversized body started %d races", got)
	}
}

// deadlineRecorder is a recorder that also carries the read deadline a real connection would take.
type deadlineRecorder struct {
	*httptest.ResponseRecorder
	readDeadlines []time.Time
}

func (w *deadlineRecorder) SetReadDeadline(deadline time.Time) error {
	w.readDeadlines = append(w.readDeadlines, deadline)
	return nil
}

// Test flow:
//  1. Read a chat body through `readBody`, using a `deadlineRecorder` that records every read deadline set.
//  2. Assert the returned body matches what was sent.
//  3. Assert exactly two deadlines were recorded: one armed and one cleared.
//  4. Assert the armed deadline sits at least `bodyReadTimeout` past when the read began.
//  5. Assert the cleared deadline is zero, so it cannot expire mid-response.
func TestAReadDeadlineBoundsTheBodyAndIsClearedBeforeTheResponse(t *testing.T) {
	writer := &deadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(chatBody))
	readStarted := time.Now()

	body, err := readBody(writer, request, chatIngestLimit)
	if err != nil {
		t.Fatalf("readBody() = %v", err)
	}
	if string(body) != chatBody {
		t.Fatalf("readBody() = %q, want the request body", body)
	}
	if len(writer.readDeadlines) != 2 {
		t.Fatalf("read deadlines set: %v, want one armed and one cleared", writer.readDeadlines)
	}
	if armed := writer.readDeadlines[0]; armed.Before(readStarted.Add(bodyReadTimeout)) {
		t.Errorf("the read was armed until %v, want at least %v past the moment it began", armed, bodyReadTimeout)
	}
	if left := writer.readDeadlines[1]; !left.IsZero() {
		t.Errorf("the deadline outlived the read as %v, so a longer response would be cut by it", left)
	}
}

// Test flow:
//  1. Define a table of declared Content-Length values, varying across exact, undeclared (-1), overstated, and understated.
//  2. For each case, read the chat body through `readBody`.
//  3. Assert the returned body always matches the whole body actually sent, regardless of the declared length.
func TestABodyIsReadWholeWhateverLengthItDeclares(t *testing.T) {
	testCases := []struct {
		name          string
		contentLength int64
	}{
		{name: "declared exactly", contentLength: int64(len(chatBody))},
		{name: "undeclared", contentLength: -1},
		{name: "overstated", contentLength: 1 << 20},
		{name: "understated", contentLength: 4},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(chatBody))
			request.ContentLength = testCase.contentLength

			body, err := readBody(httptest.NewRecorder(), request, chatIngestLimit)
			if err != nil {
				t.Fatalf("readBody() = %v", err)
			}
			if string(body) != chatBody {
				t.Fatalf("readBody() = %q, want the whole body", body)
			}
		})
	}
}

// Test flow:
//  1. Define a table of public route probes: models, status, metrics, healthz, devshard models, devshard status, and an unmatched path.
//  2. For each probe, send the request with a guessed bearer token.
//  3. Assert the admin key was never compared, so a cheap public route is not a timing oracle.
func TestThePublicRoutesNeverReachTheKeyComparison(t *testing.T) {
	probes := []struct {
		name   string
		method string
		target string
	}{
		{name: "models", method: http.MethodGet, target: "/v1/models"},
		{name: "status", method: http.MethodGet, target: "/v1/status"},
		{name: "metrics", method: http.MethodGet, target: "/metrics"},
		{name: "healthz", method: http.MethodGet, target: "/healthz"},
		{name: "devshard models", method: http.MethodGet, target: "/devshard/7/v1/models"},
		{name: "devshard status", method: http.MethodGet, target: "/devshard/7/v1/status"},
		{name: "unmatched", method: http.MethodGet, target: "/favicon.ico"},
	}
	for _, probe := range probes {
		t.Run(probe.name, func(t *testing.T) {
			live := newHarness(t)
			live.request(t, probe.method, probe.target, "", map[string]string{"Authorization": "Bearer guess"})
			if got := live.comparisons.Load(); got != 0 {
				t.Fatalf("%s compared the admin key %d times; a cheap public route must not be a timing oracle", probe.target, got)
			}
		})
	}
}

// Test flow:
//  1. Send an admin-state request with no Authorization header.
//  2. Assert the admin key was never compared.
func TestAnUnauthenticatedRequestNeverReachesTheKeyComparison(t *testing.T) {
	live := newHarness(t)
	live.request(t, http.MethodGet, "/v1/admin/state", "", nil)
	if got := live.comparisons.Load(); got != 0 {
		t.Fatalf("a request with no Authorization header compared the key %d times", got)
	}
}

// Test flow:
//  1. Create a key gate for the configured admin key.
//  2. Define a table of Authorization header values, varying across the exact key, whitespace-padded variants, the scheme in any case, a wrong key, a key prefix, a key in another case, a key with a space inside, a missing bearer scheme, a basic scheme, and an empty header.
//  3. For each case, call `gate.authenticate`.
//  4. Assert the result matches the case's expectation.
func TestTheAdminKeyGateAcceptsOnlyTheConfiguredKey(t *testing.T) {
	testCases := []struct {
		name          string
		authorization string
		want          bool
	}{
		{name: "exact", authorization: "Bearer " + adminKey, want: true},
		{name: "trailing whitespace is trimmed", authorization: "Bearer " + adminKey + "  ", want: true},
		{name: "leading whitespace is trimmed", authorization: "Bearer   " + adminKey, want: true},
		{name: "wrong key", authorization: "Bearer " + adminKey + "x"},
		{name: "prefix of the key", authorization: "Bearer " + adminKey[:4]},
		{name: "no bearer scheme", authorization: adminKey},
		{name: "basic scheme", authorization: "Basic " + adminKey},
		{name: "empty", authorization: ""},
		{name: "lowercase scheme", authorization: "bearer " + adminKey, want: true},
		{name: "uppercase scheme", authorization: "BEARER " + adminKey, want: true},
		{name: "tab after the scheme", authorization: "Bearer\t" + adminKey, want: true},
		{name: "a key with a space inside", authorization: "Bearer " + adminKey + " extra"},
		{name: "scheme alone", authorization: "Bearer"},
		{name: "key in another case", authorization: "Bearer " + strings.ToUpper(adminKey)},
	}
	gate := newKeyGate(adminKey)
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := gate.authenticate(testCase.authorization); got != testCase.want {
				t.Fatalf("authenticate(%q): got %v, want %v", testCase.authorization, got, testCase.want)
			}
		})
	}
}

// Test flow:
//  1. Create a key gate configured with only blank keys.
//  2. Assert it authenticates neither an empty bearer token nor an arbitrary one.
func TestAnUnconfiguredKeyGateAuthenticatesNothing(t *testing.T) {
	gate := newKeyGate("", "   ")
	if gate.authenticate("Bearer ") || gate.authenticate("Bearer anything") {
		t.Fatal("an unconfigured gate authenticated a request")
	}
}

// Test flow:
//  1. Send an admin-state request with a key one character off from the configured one.
//  2. Assert the response is 401.
//  3. Assert exactly one key comparison happened.
//  4. Assert the response's Content-Type is application/json.
func TestTheOperatorRoutesRefuseAWrongKey(t *testing.T) {
	live := newHarness(t)
	recorder := live.request(t, http.MethodGet, "/v1/admin/state", "", map[string]string{"Authorization": "Bearer " + adminKey + "x"})
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status: got %d, want 401", recorder.Code)
	}
	if got := live.comparisons.Load(); got != 1 {
		t.Fatalf("comparisons on an operator route: got %d, want 1", got)
	}
	if got := recorder.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type: got %q, want application/json", got)
	}
}

// Test flow:
//  1. Clear the configured admin API key.
//  2. Send an admin-state request with the now-stale admin key.
//  3. Assert the response is 404, so an unconfigured admin surface does not even reveal the route exists.
func TestTheOperatorRoutesAre404WhenNoAdminKeyIsConfigured(t *testing.T) {
	live := newHarness(t)
	live.swapConfig(func(next *config.Config) { next.Server.AdminAPIKey = "" })
	recorder := live.request(t, http.MethodGet, "/v1/admin/state", "", adminHeaders())
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status: got %d, want 404", recorder.Code)
	}
}

// Test flow:
//  1. Confirm the originally configured admin key is accepted.
//  2. Rotate the configured admin key to a new value.
//  3. Send one request with the retired key and one with the rotated key.
//  4. Assert the retired key is now refused with 401 and the rotated key is accepted with 200.
func TestARotatedAdminKeyTakesEffectOnTheNextRequest(t *testing.T) {
	live := newHarness(t)
	if served := live.request(t, http.MethodGet, "/v1/admin/state", "", adminHeaders()); served.Code != http.StatusOK {
		t.Fatalf("the configured key: got %d, want 200", served.Code)
	}

	live.swapConfig(func(next *config.Config) { next.Server.AdminAPIKey = "rotated-admin-key" })

	retired := live.request(t, http.MethodGet, "/v1/admin/state", "", adminHeaders())
	rotated := live.request(t, http.MethodGet, "/v1/admin/state", "",
		map[string]string{"Authorization": "Bearer rotated-admin-key"})

	if retired.Code != http.StatusUnauthorized {
		t.Fatalf("the retired key: got %d, want 401", retired.Code)
	}
	if rotated.Code != http.StatusOK {
		t.Fatalf("the rotated key: got %d, want 200", rotated.Code)
	}
}

// Test flow:
//  1. Configure a low default and cap on max tokens.
//  2. Send an admin-keyed chat completion whose requested max_tokens exceeds the cap.
//  3. Assert the response is 200, so the admin flag lets the request through while filters still apply.
//  4. Assert the admin key was compared exactly once.
func TestTheAdminFlagStillReachesTheRequestFilters(t *testing.T) {
	live := newHarness(t)
	live.swapConfig(func(next *config.Config) {
		next.Limits.DefaultMaxTokens = 16
		next.Limits.MaxTokensCap = 32
	})
	recorder := live.request(t, http.MethodPost, "/v1/chat/completions",
		`{"model":"qwen","max_tokens":4096,"messages":[{"role":"user","content":"hi"}]}`, adminHeaders())
	if recorder.Code != http.StatusOK {
		t.Fatalf("status: got %d (%s)", recorder.Code, recorder.Body.String())
	}
	if got := live.comparisons.Load(); got != 1 {
		t.Fatalf("the chat path must resolve the admin flag exactly once: got %d", got)
	}
}

// Test flow:
//  1. Restrict the requested model to API-key access.
//  2. Send an anonymous chat completion and assert it is refused with 401.
//  3. Send the same completion with a client API key and assert it is served with 200.
func TestAClientAPIKeyOpensAnAPIKeyGatedModel(t *testing.T) {
	live := newHarness(t)
	live.swapConfig(func(next *config.Config) {
		next.Limits.ModelAccess = map[string]string{"qwen": config.ModelAccessAPIKey}
	})
	anonymous := live.request(t, http.MethodPost, "/v1/chat/completions", chatBody, nil)
	if anonymous.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous: got %d, want 401", anonymous.Code)
	}
	authorized := live.request(t, http.MethodPost, "/v1/chat/completions", chatBody,
		map[string]string{"Authorization": "Bearer " + clientKey})
	if authorized.Code != http.StatusOK {
		t.Fatalf("with an API key: got %d (%s), want 200", authorized.Code, authorized.Body.String())
	}
}

// Test flow:
//  1. Grant open access to a different, unrelated model, leaving the requested model admin-only by omission.
//  2. Send a chat completion for the requested model with a client API key.
//  3. Assert the response is 401 and the request took no limiter slot.
func TestAnAdminOnlyModelRefusesAClientKey(t *testing.T) {
	live := newHarness(t)
	live.swapConfig(func(next *config.Config) {
		next.Limits.ModelAccess = map[string]string{"other": config.ModelAccessOpen}
	})
	recorder := live.request(t, http.MethodPost, "/v1/chat/completions", chatBody,
		map[string]string{"Authorization": "Bearer " + clientKey})
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401", recorder.Code)
	}
	if got := live.limiter.acquires.Load(); got != 0 {
		t.Fatalf("a denied request took %d limiter slots", got)
	}
}
