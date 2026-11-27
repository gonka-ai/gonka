package harness

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestenvAdminAPIKey matches DEVSHARD_ADMIN_API_KEY in gencompose .env, and GatewayEscrowKeyEnv names the
// gateway env var that holds the escrow signing key, as gencompose renders it.
const (
	gatewayChatTimeout  = 3 * time.Minute
	TestenvAdminAPIKey  = "testenv-citest-admin"
	GatewayEscrowKeyEnv = "GATEWAY_ESCROW_KEY"
)

// GatewayChatClient returns an HTTP client with a longer timeout for inference paths.
func GatewayChatClient() *http.Client {
	return &http.Client{Timeout: gatewayChatTimeout}
}

// ChatCompletionRequest is a minimal OpenAI chat payload for gateway citest.
type ChatCompletionRequest struct {
	Model         string             `json:"model"`
	Messages      []ChatMessage      `json:"messages"`
	MaxTokens     int                `json:"max_tokens,omitempty"`
	Stream        bool               `json:"stream,omitempty"`
	StreamOptions *ChatStreamOptions `json:"stream_options,omitempty"`
	Logprobs      *bool              `json:"logprobs,omitempty"`
	TopLogprobs   *int               `json:"top_logprobs,omitempty"`
	Seed          *int               `json:"seed,omitempty"`
}

// ChatStreamOptions is the OpenAI stream_options object.
type ChatStreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

// ChatMessage is one chat message.
type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ChatCompletionResponse is the non-stream OpenAI-shaped JSON body.
type ChatCompletionResponse struct {
	Object  string `json:"object"`
	Model   string `json:"model"`
	Choices []struct {
		Message struct {
			Content string `json:"content"`
			Role    string `json:"role"`
		} `json:"message"`
		FinishReason string          `json:"finish_reason"`
		Logprobs     json.RawMessage `json:"logprobs"`
	} `json:"choices"`
	Usage map[string]any `json:"usage"`
}

// GatewayChatHTTPResult is the raw HTTP response from /v1/chat/completions.
type GatewayChatHTTPResult struct {
	Status      int
	ContentType string
	Body        []byte
	Header      http.Header
}

// LogprobsAsk names an explicit ask, false included, which a bare bool would drop as empty.
func LogprobsAsk(asked bool) *bool { return &asked }

// TopLogprobsWidth names an explicit width, zero included, which a bare int would drop as empty.
func TopLogprobsWidth(width int) *int { return &width }

// PostGatewayChatCompletion posts non-stream /v1/chat/completions and requires HTTP 200.
func PostGatewayChatCompletion(t *testing.T, client *http.Client, gatewayURL, adminAPIKey string, req ChatCompletionRequest) ChatCompletionResponse {
	t.Helper()
	if client == nil {
		client = GatewayChatClient()
	}
	var resp ChatCompletionResponse
	require.NoError(t, PostGatewayJSON(client, gatewayURL+"/v1/chat/completions", adminAPIKey, req, &resp))
	require.NotEmpty(t, resp.Choices, "gateway chat returned no choices")
	require.NotEmpty(t, resp.Choices[0].Message.Content, "empty assistant content")
	return resp
}

// TryPostGatewayChatCompletion is like PostGatewayChatCompletion but returns an error
// instead of failing the test (safe from worker goroutines).
func TryPostGatewayChatCompletion(client *http.Client, gatewayURL, adminAPIKey string, req ChatCompletionRequest) (ChatCompletionResponse, error) {
	if client == nil {
		client = GatewayChatClient()
	}
	var resp ChatCompletionResponse
	if err := PostGatewayJSON(client, gatewayURL+"/v1/chat/completions", adminAPIKey, req, &resp); err != nil {
		return resp, err
	}
	if len(resp.Choices) == 0 {
		return resp, fmt.Errorf("gateway chat returned no choices")
	}
	if resp.Choices[0].Message.Content == "" {
		return resp, fmt.Errorf("empty assistant content")
	}
	return resp, nil
}

// GatewayCapacityGone is a dead escrow/runtime: further chat cannot create work.
func GatewayCapacityGone(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "no devshard runtimes available") ||
		strings.Contains(msg, "insufficient escrow balance")
}

// PostGatewayChatCompletionEventually retries chat while hosts recover from a
// full versiond restart (limiter may still report no live capacity).
func PostGatewayChatCompletionEventually(t *testing.T, client *http.Client, gatewayURL, adminAPIKey string, req ChatCompletionRequest, timeout time.Duration) ChatCompletionResponse {
	t.Helper()
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	deadline := time.Now().Add(timeout)
	var last error
	for time.Now().Before(deadline) {
		resp, err := TryPostGatewayChatCompletion(client, gatewayURL, adminAPIKey, req)
		if err == nil {
			return resp
		}
		last = err
		msg := err.Error()
		if !strings.Contains(msg, "no live host capacity") && !strings.Contains(msg, "503") {
			require.NoError(t, err)
		}
		t.Logf("citest: waiting for live host capacity: %v", err)
		time.Sleep(2 * time.Second)
	}
	require.NoError(t, last, "gateway chat did not recover after %s", timeout)
	return ChatCompletionResponse{}
}

// PostGatewayChatCompletionStream posts stream=true and collects SSE until [DONE].
func PostGatewayChatCompletionStream(t *testing.T, client *http.Client, gatewayURL, adminAPIKey string, req ChatCompletionRequest) (content string, sawDone bool) {
	t.Helper()
	if client == nil {
		client = GatewayChatClient()
	}
	req.Stream = true
	data, err := json.Marshal(req)
	require.NoError(t, err)

	httpReq, err := http.NewRequest(http.MethodPost, gatewayURL+"/v1/chat/completions", bytes.NewReader(data))
	require.NoError(t, err)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	setAdminBearer(httpReq, adminAPIKey)

	resp, err := client.Do(httpReq)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode, "stream chat: %s", string(body))

	ct := resp.Header.Get("Content-Type")
	require.True(t, strings.Contains(ct, "text/event-stream") || strings.Contains(ct, "event-stream"),
		"expected SSE content-type, got %q", ct)

	var assembled strings.Builder
	scanner := bufio.NewScanner(bytes.NewReader(body))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "data: [DONE]" {
			sawDone = true
			continue
		}
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		var chunk map[string]any
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			continue
		}
		choices, _ := chunk["choices"].([]any)
		if len(choices) == 0 {
			continue
		}
		choice, _ := choices[0].(map[string]any)
		delta, _ := choice["delta"].(map[string]any)
		if delta == nil {
			continue
		}
		if s, ok := delta["content"].(string); ok {
			assembled.WriteString(s)
		}
	}
	require.NoError(t, scanner.Err())
	require.True(t, sawDone, "stream missing data: [DONE]")
	content = assembled.String()
	require.NotEmpty(t, content, "stream assembled empty content")
	return content, sawDone
}

// StreamChatTimed is the result of an incremental SSE chat: FirstToken is set
// when the first content delta arrives (gateway first-token floor signal).
type StreamChatTimed struct {
	Content    string
	FirstToken time.Duration // 0 if no content delta was seen
	SawDone    bool
}

// GatewayFirstTokenFloor is the default redundancy first-token timeout floor
// (~1s). Timed stream probes under SlowMockOpenAI should land well under this.
const GatewayFirstTokenFloor = time.Second

// TryPostGatewayChatCompletionStream is the worker-safe stream variant of
// PostGatewayChatCompletionStream. Under SlowMockOpenAI, latency_ms delays
// Validate only; with stream_ttft_ms=0 the first SSE chunk flushes immediately
// so the gateway first-token floor stays green (client stream or non-stream).
func TryPostGatewayChatCompletionStream(client *http.Client, gatewayURL, adminAPIKey string, req ChatCompletionRequest) (content string, err error) {
	res, err := TryPostGatewayChatCompletionStreamTimed(client, gatewayURL, adminAPIKey, req)
	return res.Content, err
}

// TryPostGatewayChatCompletionStreamTimed streams the response incrementally
// and records time-to-first-content-delta before draining to [DONE].
func TryPostGatewayChatCompletionStreamTimed(client *http.Client, gatewayURL, adminAPIKey string, req ChatCompletionRequest) (StreamChatTimed, error) {
	var out StreamChatTimed
	if client == nil {
		client = GatewayChatClient()
	}
	req.Stream = true
	data, err := json.Marshal(req)
	if err != nil {
		return out, err
	}
	httpReq, err := http.NewRequest(http.MethodPost, gatewayURL+"/v1/chat/completions", bytes.NewReader(data))
	if err != nil {
		return out, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	setAdminBearer(httpReq, adminAPIKey)
	start := time.Now()
	resp, err := client.Do(httpReq)
	if err != nil {
		return out, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return out, fmt.Errorf("POST %s: %d %s", gatewayURL+"/v1/chat/completions", resp.StatusCode, string(body))
	}
	var assembled strings.Builder
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 2*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "data: [DONE]" {
			out.SawDone = true
			continue
		}
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		var chunk map[string]any
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			continue
		}
		choices, _ := chunk["choices"].([]any)
		if len(choices) == 0 {
			continue
		}
		choice, _ := choices[0].(map[string]any)
		delta, _ := choice["delta"].(map[string]any)
		if delta == nil {
			continue
		}
		if s, ok := delta["content"].(string); ok && s != "" {
			if out.FirstToken == 0 {
				out.FirstToken = time.Since(start)
			}
			assembled.WriteString(s)
		}
	}
	if err := scanner.Err(); err != nil {
		return out, err
	}
	out.Content = assembled.String()
	if !out.SawDone {
		return out, fmt.Errorf("stream missing data: [DONE]")
	}
	if out.Content == "" {
		return out, fmt.Errorf("stream assembled empty content")
	}
	if out.FirstToken == 0 {
		return out, fmt.Errorf("stream completed without a content delta")
	}
	return out, nil
}

// PostGatewayChatHTTP posts /v1/chat/completions and returns status, headers, and body.
// Unlike PostGatewayChatCompletion it does not require HTTP 200.
func PostGatewayChatHTTP(t *testing.T, client *http.Client, gatewayURL, adminAPIKey string, req ChatCompletionRequest) GatewayChatHTTPResult {
	t.Helper()
	result, err := postGatewayChatHTTP(client, gatewayURL, adminAPIKey, req)
	require.NoError(t, err)
	return result
}

func postGatewayChatHTTP(client *http.Client, gatewayURL, adminAPIKey string, req ChatCompletionRequest) (GatewayChatHTTPResult, error) {
	if client == nil {
		client = GatewayChatClient()
	}
	data, err := json.Marshal(req)
	if err != nil {
		return GatewayChatHTTPResult{}, err
	}
	httpReq, err := http.NewRequest(http.MethodPost, gatewayURL+"/v1/chat/completions", bytes.NewReader(data))
	if err != nil {
		return GatewayChatHTTPResult{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if req.Stream {
		httpReq.Header.Set("Accept", "text/event-stream")
	}
	setAdminBearer(httpReq, adminAPIKey)
	resp, err := client.Do(httpReq)
	if err != nil {
		return GatewayChatHTTPResult{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return GatewayChatHTTPResult{}, err
	}
	return GatewayChatHTTPResult{
		Status:      resp.StatusCode,
		ContentType: resp.Header.Get("Content-Type"),
		Body:        body,
		Header:      resp.Header.Clone(),
	}, nil
}

// ParseSSEDataChunks returns JSON payloads from SSE data: lines (excluding [DONE]).
func ParseSSEDataChunks(body []byte) ([]map[string]any, bool) {
	var chunks []map[string]any
	sawDone := false
	scanner := bufio.NewScanner(bytes.NewReader(body))
	scanner.Buffer(make([]byte, 0, 64*1024), 2*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "data: [DONE]" {
			sawDone = true
			continue
		}
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		var chunk map[string]any
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			continue
		}
		chunks = append(chunks, chunk)
	}
	return chunks, sawDone
}

// AssembleSSEContent concatenates delta.content from SSE chunks.
func AssembleSSEContent(chunks []map[string]any) string {
	var assembled strings.Builder
	for _, chunk := range chunks {
		choices, _ := chunk["choices"].([]any)
		if len(choices) == 0 {
			continue
		}
		choice, _ := choices[0].(map[string]any)
		delta, _ := choice["delta"].(map[string]any)
		if delta == nil {
			continue
		}
		if s, ok := delta["content"].(string); ok {
			assembled.WriteString(s)
		}
	}
	return assembled.String()
}

// RequireMockOpenAIContent asserts assistant text came from mock-openai echo; a small max_tokens cuts
// the echo inside its "mock-openai:" prefix.
func RequireMockOpenAIContent(t *testing.T, content string) {
	t.Helper()
	require.True(t, content != "" && (strings.HasPrefix(content, "mock-openai:") || strings.HasPrefix("mock-openai:", content)),
		"expected mock-openai echo, got %q", content)
}

// PostGatewayJSON posts an authenticated JSON request to a gateway endpoint and decodes the response.
func PostGatewayJSON(client *http.Client, url, adminAPIKey string, payload, dest any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	setAdminBearer(req, adminAPIKey)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode >= http.StatusMultipleChoices {
		return &GatewayStatusError{Method: http.MethodPost, URL: url, StatusCode: resp.StatusCode, Body: string(body)}
	}
	if dest == nil {
		return nil
	}
	return json.Unmarshal(body, dest)
}

// WaitGatewayChatReady waits for catalog admission first (when stack is
// provided), then polls GET /v1/status until the gateway lists a routable escrow. Catalog must come
// before treating the gateway as chat-ready: heartbeats and warmup POST
// /chat/completions, and a 503 undeclared_version quarantines the host. It
// does not POST /v1/chat/completions.
func WaitGatewayChatReady(t *testing.T, client *http.Client, gatewayURL string, timeout time.Duration, stack ...*Stack) {
	t.Helper()
	if client == nil {
		client = GatewayChatClient()
	}
	if timeout == 0 {
		timeout = 3 * time.Minute
	}
	if len(stack) > 0 && stack[0] != nil {
		WaitRouterCatalogAdmitted(t, stack[0], timeout)
	}
	t.Logf("citest: waiting for a routable gateway escrow → %s/v1/status", gatewayURL)
	var attempts int
	var lastDetail string
	ok := assertEventually(t, timeout, 2*time.Second, func() bool {
		attempts++
		escrows, err := GetGatewayStatusEscrows(client, gatewayURL)
		if err != nil {
			lastDetail = err.Error()
			maybeLogWaitAttempt(t, "routable gateway escrow", attempts, lastDetail)
			return false
		}
		if !gatewayStatusListsRoutableEscrow(escrows) {
			lastDetail = fmt.Sprintf("no routable escrow in status: %+v", escrows)
			maybeLogWaitAttempt(t, "routable gateway escrow", attempts, lastDetail)
			return false
		}
		return true
	})
	if !ok {
		if len(stack) > 0 && stack[0] != nil {
			DumpComposeLogs(t, stack[0], "gateway", "mock-chain", "versiond-0", "versiond-1")
		}
		t.Fatalf("citest: gateway lists no routable escrow after %d attempts: %s", attempts, lastDetail)
	}
}

// WaitRouterCatalogAdmitted polls GET /{version}/healthz on the versiond router.
// That is the catalog-admission signal; it is not an inference request.
// Uses RouterHTTP so it works while the gateway is still down.
func WaitRouterCatalogAdmitted(t *testing.T, stack *Stack, timeout time.Duration) {
	t.Helper()
	if stack == nil {
		return
	}
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	cfg := stack.LoadConfig(t)
	ver := strings.TrimSpace(cfg.Versiond.VersionName)
	if ver == "" {
		return
	}
	routerHTTP := stack.RouterHTTP(t)
	WaitGETOK(t, HTTPClient(), routerCatalogHealthzURL(routerHTTP, ver), timeout, "devshardd health via router (catalog)", stack)
}

func routerCatalogHealthzURL(routerHTTP, version string) string {
	return strings.TrimRight(routerHTTP, "/") + "/" + version + "/healthz"
}

func gatewayStatusListsRoutableEscrow(escrows []gatewayStatusEscrow) bool {
	for _, escrow := range escrows {
		if escrow.EscrowID != noEscrowID {
			return true
		}
	}
	return false
}
