package engineinf_test

import (
	inference "common/engineinf"
	"common/engineinf/testutil"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubPayloadWriter records calls to StorePayload.
type stubPayloadWriter struct {
	called bool
	prompt []byte
	resp   []byte
}

func (s *stubPayloadWriter) StorePayload(_ context.Context, _ string, _ uint64, prompt, resp []byte) error {
	s.called = true
	s.prompt = prompt
	s.resp = resp
	return nil
}

// buildExecutorEchoContext creates an Echo context that writes into a recorder.
func buildExecutorEchoContext(body string) (echo.Context, *httptest.ResponseRecorder) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	return e.NewContext(req, rec), rec
}

func TestExecutor_Handle_ProxiesSSEResponse(t *testing.T) {
	// Start a fake ML node that returns SSE.
	mlNode := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"id\":\"1\"}\n\ndata: [DONE]\n\n"))
	}))
	defer mlNode.Close()

	lock := testutil.NewStubNodeLock(mlNode.URL, "lock-1")
	ex := &inference.Executor{Lock: lock}

	rawBody := []byte(`{"model":"llama3","messages":[{"role":"user","content":"hi"}]}`)
	c, rec := buildExecutorEchoContext(string(rawBody))

	err := ex.Handle(c, "inf-123", "llama3", rawBody)
	require.NoError(t, err)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Header().Get("Content-Type"), "text/event-stream")
	assert.Contains(t, rec.Body.String(), "[DONE]")
}

func TestExecutor_Handle_NilStorage_NoPanic(t *testing.T) {
	mlNode := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"choices":[]}`)
	}))
	defer mlNode.Close()

	lock := testutil.NewStubNodeLock(mlNode.URL, "lock-2")
	ex := &inference.Executor{Lock: lock} // Storage is nil

	rawBody := []byte(`{"model":"llama3","messages":[]}`)
	c, rec := buildExecutorEchoContext(string(rawBody))

	require.NotPanics(t, func() {
		_ = ex.Handle(c, "inf-456", "llama3", rawBody)
	})
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestExecutor_Handle_StoresPayload(t *testing.T) {
	responseBody := `{"choices":[]}`
	mlNode := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, responseBody)
	}))
	defer mlNode.Close()

	lock := testutil.NewStubNodeLock(mlNode.URL, "lock-3")
	storage := &stubPayloadWriter{}
	ex := &inference.Executor{Lock: lock, Storage: storage}

	rawBody := []byte(`{"model":"llama3","messages":[{"role":"user","content":"hello"}]}`)
	c, _ := buildExecutorEchoContext(string(rawBody))

	err := ex.Handle(c, "inf-789", "llama3", rawBody)
	require.NoError(t, err)

	assert.True(t, storage.called, "StorePayload should have been called")
	assert.Equal(t, rawBody, storage.prompt)
	assert.Equal(t, []byte(responseBody), storage.resp)
}

func TestModifyRequest_SetsLogprobFields(t *testing.T) {
	input := []byte(`{"model":"llama3","messages":[]}`)
	out, err := inference.ModifyRequest(input)
	require.NoError(t, err)

	var result map[string]any
	require.NoError(t, json.Unmarshal(out, &result))

	assert.Equal(t, true, result["logprobs"])
	assert.Equal(t, float64(5), result["top_logprobs"])
}

func TestModifyRequest_SyncsMaxTokens_FromCompletion(t *testing.T) {
	input := []byte(`{"model":"llama3","messages":[],"max_completion_tokens":100}`)
	out, err := inference.ModifyRequest(input)
	require.NoError(t, err)

	var result map[string]any
	require.NoError(t, json.Unmarshal(out, &result))

	assert.Equal(t, float64(100), result["max_tokens"], "max_tokens should be synced from max_completion_tokens")
	assert.Equal(t, float64(100), result["max_completion_tokens"])
}

func TestModifyRequest_SyncsMaxTokens_FromMaxTokens(t *testing.T) {
	input := []byte(`{"model":"llama3","messages":[],"max_tokens":200}`)
	out, err := inference.ModifyRequest(input)
	require.NoError(t, err)

	var result map[string]any
	require.NoError(t, json.Unmarshal(out, &result))

	assert.Equal(t, float64(200), result["max_tokens"])
	assert.Equal(t, float64(200), result["max_completion_tokens"], "max_completion_tokens should be synced from max_tokens")
}

func TestModifyRequest_BothPresent_NoChange(t *testing.T) {
	input := []byte(`{"model":"llama3","messages":[],"max_tokens":100,"max_completion_tokens":200}`)
	out, err := inference.ModifyRequest(input)
	require.NoError(t, err)

	var result map[string]any
	require.NoError(t, json.Unmarshal(out, &result))

	// When both are present, neither is overwritten.
	assert.Equal(t, float64(100), result["max_tokens"])
	assert.Equal(t, float64(200), result["max_completion_tokens"])
}

func TestModifyRequest_InvalidJSON_SafeFallback(t *testing.T) {
	input := []byte(`not json`)
	out, err := inference.ModifyRequest(input)
	require.NoError(t, err)
	assert.Equal(t, input, out)
}

// freshTimestamp returns a nanosecond Unix timestamp that passes ValidateTimestamp.
func freshTimestamp() string {
	return strings.TrimSpace(strings.Replace(
		strings.Replace(
			time.Now().UTC().Format(time.RFC3339Nano),
			"T", " ", 1,
		),
		"Z", "", 1,
	))
}
