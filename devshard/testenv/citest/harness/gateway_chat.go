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

const gatewayChatTimeout = 3 * time.Minute

// TestenvAdminAPIKey matches DEVSHARD_ADMIN_API_KEY in gencompose .env.
const TestenvAdminAPIKey = "testenv-citest-admin"

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
	require.NoError(t, postGatewayJSON(client, gatewayURL+"/v1/chat/completions", adminAPIKey, req, &resp))
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
	if err := postGatewayJSON(client, gatewayURL+"/v1/chat/completions", adminAPIKey, req, &resp); err != nil {
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
	if adminAPIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+adminAPIKey)
	}

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
	if adminAPIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+adminAPIKey)
	}
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

// RequireMockOpenAIContent asserts assistant text came from mock-openai echo.
func RequireMockOpenAIContent(t *testing.T, content string) {
	t.Helper()
	require.True(t, strings.HasPrefix(content, "mock-openai:"), "expected mock-openai echo, got %q", content)
}

func postGatewayJSON(client *http.Client, url, adminAPIKey string, payload, dest any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if adminAPIKey != "" {
		req.Header.Set("Authorization", "Bearer "+adminAPIKey)
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("POST %s: %d %s", url, resp.StatusCode, string(body))
	}
	if dest == nil {
		return nil
	}
	return json.Unmarshal(body, dest)
}

// WaitGatewayChatReady waits for catalog admission first (when stack is
// provided), then polls GET /v1/status until the gateway reports a runtime
// (escrow_id, runtimes > 0, or a non-empty devshards list). Catalog must come
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
	t.Logf("citest: waiting for gateway chat runtime → %s/v1/status", gatewayURL)
	var attempts int
	var lastDetail string
	ok := assertEventually(t, timeout, 2*time.Second, func() bool {
		attempts++
		var status map[string]any
		if err := GetJSON(client, gatewayURL+"/v1/status", &status); err != nil {
			lastDetail = err.Error()
			maybeLogWaitAttempt(t, "gateway chat runtime", attempts, lastDetail)
			return false
		}
		if !gatewayStatusHasRuntime(status) {
			lastDetail = fmt.Sprintf("no active runtime in status: %v", status)
			maybeLogWaitAttempt(t, "gateway chat runtime", attempts, lastDetail)
			return false
		}
		if !gatewayStatusHeightSeedReady(status) {
			lastDetail = fmt.Sprintf("height seed not ready in status: %v", status)
			maybeLogWaitAttempt(t, "gateway height seed", attempts, lastDetail)
			return false
		}
		return true
	})
	if !ok {
		if len(stack) > 0 && stack[0] != nil {
			DumpComposeLogs(t, stack[0], "devshardctl", "mock-chain", "versiond-0", "versiond-1")
		}
		t.Fatalf("citest: gateway chat runtime not ready after %d attempts: %s", attempts, lastDetail)
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

func gatewayStatusHasRuntime(status map[string]any) bool {
	if status == nil {
		return false
	}
	if _, ok := status["escrow_id"]; ok {
		return true
	}
	if runtimes, ok := status["runtimes"].(float64); ok && runtimes > 0 {
		return true
	}
	if devshards, ok := status["devshards"].([]any); ok && len(devshards) > 0 {
		return true
	}
	return false
}

func gatewayStatusHeightSeedReady(status map[string]any) bool {
	if status == nil {
		return true
	}
	if !heightSeedValueReady(status["height_seed"]) {
		return false
	}
	devshards, ok := status["devshards"].([]any)
	if !ok {
		return true
	}
	for _, raw := range devshards {
		item, _ := raw.(map[string]any)
		if item == nil {
			continue
		}
		if !heightSeedValueReady(item["height_seed"]) {
			return false
		}
	}
	return true
}

func heightSeedValueReady(v any) bool {
	if v == nil {
		return true
	}
	m, ok := v.(map[string]any)
	if !ok {
		return true
	}
	state, _ := m["state"].(string)
	switch state {
	case "", "ok":
		return true
	default:
		return false
	}
}
