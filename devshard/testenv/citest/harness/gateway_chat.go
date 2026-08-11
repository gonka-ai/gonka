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
	Model         string              `json:"model"`
	Messages      []ChatMessage       `json:"messages"`
	MaxTokens     int                 `json:"max_tokens,omitempty"`
	Stream        bool                `json:"stream,omitempty"`
	StreamOptions *ChatStreamOptions  `json:"stream_options,omitempty"`
	Logprobs      bool                `json:"logprobs,omitempty"`
	TopLogprobs   int                 `json:"top_logprobs,omitempty"`
	Seed          *int                `json:"seed,omitempty"`
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

// RequireMockOpenAIContent asserts assistant text came from mock-openai echo.
func RequireMockOpenAIContent(t *testing.T, content string) {
	t.Helper()
	require.True(t, strings.HasPrefix(content, "mock-openai:"), "expected mock-openai echo, got %q", content)
}

// TryPostGatewayChatCompletionStream is like PostGatewayChatCompletionStream but
// returns an error instead of failing the test (for expected-failure paths).
func TryPostGatewayChatCompletionStream(client *http.Client, gatewayURL, adminAPIKey string, req ChatCompletionRequest) (content string, err error) {
	if client == nil {
		client = GatewayChatClient()
	}
	req.Stream = true
	data, err := json.Marshal(req)
	if err != nil {
		return "", err
	}
	httpReq, err := http.NewRequest(http.MethodPost, gatewayURL+"/v1/chat/completions", bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	if adminAPIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+adminAPIKey)
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("stream chat: %d %s", resp.StatusCode, string(body))
	}
	var assembled strings.Builder
	var sawDone bool
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
	if err := scanner.Err(); err != nil {
		return assembled.String(), err
	}
	content = assembled.String()
	if !sawDone {
		return content, fmt.Errorf("stream missing data: [DONE]")
	}
	if content == "" {
		return content, fmt.Errorf("stream assembled empty content")
	}
	return content, nil
}

// PostGatewayChatHTTP posts /v1/chat/completions and returns status, headers, and body.
// Unlike PostGatewayChatCompletion it does not require HTTP 200.
func PostGatewayChatHTTP(t *testing.T, client *http.Client, gatewayURL, adminAPIKey string, req ChatCompletionRequest) GatewayChatHTTPResult {
	t.Helper()
	if client == nil {
		client = GatewayChatClient()
	}
	data, err := json.Marshal(req)
	require.NoError(t, err)
	httpReq, err := http.NewRequest(http.MethodPost, gatewayURL+"/v1/chat/completions", bytes.NewReader(data))
	require.NoError(t, err)
	httpReq.Header.Set("Content-Type", "application/json")
	if req.Stream {
		httpReq.Header.Set("Accept", "text/event-stream")
	}
	if adminAPIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+adminAPIKey)
	}
	resp, err := client.Do(httpReq)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return GatewayChatHTTPResult{
		Status:      resp.StatusCode,
		ContentType: resp.Header.Get("Content-Type"),
		Body:        body,
		Header:      resp.Header.Clone(),
	}
}

// ParseSSEDataChunks returns JSON payloads from SSE data: lines (excluding [DONE]).
func ParseSSEDataChunks(body []byte) ([]map[string]any, bool) {
	var chunks []map[string]any
	sawDone := false
	scanner := bufio.NewScanner(bytes.NewReader(body))
	// Large SSE events (padded mock responses) can exceed the default 64 KiB buffer.
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

// WaitGatewayChatReady polls /v1/status until the gateway has a routable devshard runtime.
func WaitGatewayChatReady(t *testing.T, client *http.Client, gatewayURL string, timeout time.Duration, stack ...*Stack) {
	t.Helper()
	if client == nil {
		client = GatewayChatClient()
	}
	if timeout == 0 {
		timeout = 3 * time.Minute
	}
	t.Logf("citest: waiting for gateway chat runtime → %s/v1/status", gatewayURL)
	var attempts int
	var lastDetail string
	ok := assertEventually(t, timeout, 2*time.Second, func() bool {
		attempts++
		var status map[string]any
		if err := GetJSON(client, gatewayURL+"/v1/status", &status); err != nil {
			lastDetail = err.Error()
			return false
		}
		if gatewayStatusHasRuntime(status) {
			return true
		}
		lastDetail = fmt.Sprintf("no active runtime in status: %v", status)
		return false
	})
	if !ok {
		if len(stack) > 0 && stack[0] != nil {
			DumpComposeLogs(t, stack[0], "devshardctl", "mock-chain", "versiond-0", "versiond-1")
		}
		t.Fatalf("citest: gateway chat runtime not ready after %d attempts: %s", attempts, lastDetail)
	}
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
