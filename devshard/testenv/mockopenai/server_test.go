package mockopenai_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"common/completionapi"
	"common/validation"
	"devshard/testenv/mockopenai"

	"github.com/stretchr/testify/require"
)

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(mockopenai.NewServer(mockopenai.DefaultConfig()).Handler())
}

func TestChatCompletions_JSONDeterministic(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	body := []byte(`{"model":"test-model","messages":[{"role":"user","content":"hello"}]}`)
	var firstContent string
	for i := 0; i < 2; i++ {
		resp, err := http.Post(srv.URL+"/v1/chat/completions", "application/json", bytes.NewReader(body))
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode)
		raw, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		_ = resp.Body.Close()

		var out map[string]any
		require.NoError(t, json.Unmarshal(raw, &out))
		choices, ok := out["choices"].([]any)
		require.True(t, ok)
		require.Len(t, choices, 1)
		choice := choices[0].(map[string]any)
		msg := choice["message"].(map[string]any)
		content := msg["content"].(string)
		if i == 0 {
			firstContent = content
			require.True(t, strings.HasPrefix(content, "mock-openai:"))
		} else {
			require.Equal(t, firstContent, content)
		}
	}
}

func TestChatCompletions_StreamCompletionAPI(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	body := []byte(`{"model":"test-model","stream":true,"messages":[{"role":"user","content":"stream me"}]}`)
	resp, err := http.Post(srv.URL+"/v1/chat/completions", "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, resp.Header.Get("Content-Type"), "text/event-stream")

	var lines []string
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	require.NoError(t, sc.Err())
	_ = resp.Body.Close()
	require.NotEmpty(t, lines)

	proc := completionapi.NewExecutorResponseProcessor("inference-test")
	var streamed []string
	for _, line := range lines {
		updated, err := proc.ProcessStreamedResponse(line)
		require.NoError(t, err)
		streamed = append(streamed, updated)
	}
	cr, err := proc.GetResponse()
	require.NoError(t, err)
	content, err := cr.GetEnforcedStr()
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(content, "mock-openai:"))
}

func TestChatCompletions_MaxTokensPadsDeterministicContent(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	body := []byte(`{"model":"test-model","max_tokens":64,"messages":[{"role":"user","content":"pad me"}]}`)
	resp, err := http.Post(srv.URL+"/v1/chat/completions", "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	_ = resp.Body.Close()
	var out map[string]any
	require.NoError(t, json.Unmarshal(raw, &out))
	content := out["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)["content"].(string)
	require.True(t, strings.HasPrefix(content, "mock-openai:"))
	require.Equal(t, 64, len([]rune(content)))
}

func TestChatCompletions_EmitsLogprobsWhenRequested(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	body := []byte(`{"model":"test-model","logprobs":true,"top_logprobs":3,"messages":[{"role":"user","content":"lp"}]}`)
	resp, err := http.Post(srv.URL+"/v1/chat/completions", "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var out map[string]any
	require.NoError(t, json.Unmarshal(raw, &out))
	lp := out["choices"].([]any)[0].(map[string]any)["logprobs"].(map[string]any)
	content := lp["content"].([]any)
	require.NotEmpty(t, content)
	entry := content[0].(map[string]any)
	requireNumericTokenID(t, entry["token"])
	tops := entry["top_logprobs"].([]any)
	require.Len(t, tops, 3)
	for _, top := range tops {
		requireNumericTokenID(t, top.(map[string]any)["token"])
	}
	bytesField, ok := entry["bytes"].([]any)
	require.True(t, ok, "bytes must be a JSON array of ints, not base64: %#v", entry["bytes"])
	require.NotEmpty(t, bytesField)

	cr, err := completionapi.NewCompletionResponseFromBytes(raw)
	require.NoError(t, err)
	enforced, err := cr.GetEnforcedTokens()
	require.NoError(t, err)
	require.False(t, validation.HasNonNumericTokens(enforced),
		"validators reject decoded-text logprobs before ML replay: %+v", enforced)
}

func TestChatCompletions_StreamLogprobTokensAreNumericIDs(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	body := []byte(`{"model":"test-model","stream":true,"logprobs":true,"top_logprobs":5,"messages":[{"role":"user","content":"stream lp"}]}`)
	resp, err := http.Post(srv.URL+"/v1/chat/completions", "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var lines []string
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	require.NoError(t, sc.Err())
	_ = resp.Body.Close()

	proc := completionapi.NewExecutorResponseProcessor("inference-stream-lp")
	for _, line := range lines {
		_, err := proc.ProcessStreamedResponse(line)
		require.NoError(t, err)
	}
	cr, err := proc.GetResponse()
	require.NoError(t, err)
	enforced, err := cr.GetEnforcedTokens()
	require.NoError(t, err)
	require.NotEmpty(t, enforced.Tokens)
	require.False(t, validation.HasNonNumericTokens(enforced),
		"streamed logprobs must be token IDs: %+v", enforced)
}

func requireNumericTokenID(t *testing.T, raw any) {
	t.Helper()
	s, ok := raw.(string)
	require.True(t, ok, "token must be a JSON string, got %#v", raw)
	n, err := strconv.Atoi(s)
	require.NoError(t, err, "token %q is not a numeric id", s)
	require.GreaterOrEqual(t, n, 0)
}

func TestChatCompletions_FaultHTTPStatus(t *testing.T) {
	srv := httptest.NewServer(mockopenai.NewServer(mockopenai.Config{
		Faults: mockopenai.FaultConfig{HTTPStatus: 503},
	}).Handler())
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/v1/chat/completions", "application/json",
		bytes.NewReader([]byte(`{"messages":[{"role":"user","content":"x"}]}`)))
	require.NoError(t, err)
	require.Equal(t, 503, resp.StatusCode)
	_ = resp.Body.Close()
}

func TestChatCompletions_FaultPatch(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	patch, _ := json.Marshal(map[string]int{"http_status": 500})
	resp, err := http.Post(srv.URL+"/testenv/fault", "application/json", bytes.NewReader(patch))
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	_ = resp.Body.Close()

	resp, err = http.Post(srv.URL+"/v1/chat/completions", "application/json",
		bytes.NewReader([]byte(`{"messages":[{"role":"user","content":"fail"}]}`)))
	require.NoError(t, err)
	require.Equal(t, 500, resp.StatusCode)
	_ = resp.Body.Close()
}
