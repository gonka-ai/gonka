package devshard

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"decentralized-api/completionapi"
	"decentralized-api/utils"

	devshardpkg "devshard"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type interruptedBody struct {
	data []byte
	read bool
}

func (b *interruptedBody) Read(p []byte) (int, error) {
	if b.read {
		return 0, errors.New("stream interrupted")
	}
	b.read = true
	return copy(p, b.data), nil
}

func (b *interruptedBody) Close() error { return nil }

func TestProcessHTTPResponse_SSE(t *testing.T) {
	body := "data: {\"id\":\"1\",\"choices\":[]}\n\ndata: [DONE]\n"
	resp := &http.Response{
		Header: http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:   io.NopCloser(bytes.NewBufferString(body)),
	}
	processor := completionapi.NewExecutorResponseProcessor("")
	err := completionapi.ProcessHTTPResponse(resp, processor)
	require.NoError(t, err)

	respBytes, err := processor.GetResponseBytes()
	require.NoError(t, err)
	assert.NotEmpty(t, respBytes)
}

func TestProcessHTTPResponse_SSEWithCharset(t *testing.T) {
	body := "data: {\"id\":\"1\",\"choices\":[]}\n\ndata: [DONE]\n"
	resp := &http.Response{
		Header: http.Header{"Content-Type": []string{"text/event-stream; charset=utf-8"}},
		Body:   io.NopCloser(bytes.NewBufferString(body)),
	}
	processor := completionapi.NewExecutorResponseProcessor("")
	err := completionapi.ProcessHTTPResponse(resp, processor)
	require.NoError(t, err)

	respBytes, err := processor.GetResponseBytes()
	require.NoError(t, err)
	assert.NotEmpty(t, respBytes)
}

func TestProcessHTTPResponse_JSON(t *testing.T) {
	jsonBody := `{"id":"test","choices":[{"message":{"content":"hello"}}]}`
	resp := &http.Response{
		Header: http.Header{"Content-Type": []string{"application/json"}},
		Body:   io.NopCloser(bytes.NewBufferString(jsonBody)),
	}
	processor := completionapi.NewExecutorResponseProcessor("")
	err := completionapi.ProcessHTTPResponse(resp, processor)
	require.NoError(t, err)

	respBytes, err := processor.GetResponseBytes()
	require.NoError(t, err)
	assert.Contains(t, string(respBytes), "hello")
}

func TestSSEScanner(t *testing.T) {
	// Verify bufio.Scanner correctly handles SSE bodies with blank lines.
	body := "data: {\"id\":\"1\"}\n\ndata: {\"id\":\"2\"}\n\ndata: [DONE]\n"
	resp := &http.Response{
		Header: http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:   io.NopCloser(bytes.NewBufferString(body)),
	}
	processor := completionapi.NewExecutorResponseProcessor("")
	err := completionapi.ProcessHTTPResponse(resp, processor)
	require.NoError(t, err)

	respBytes, err := processor.GetResponseBytes()
	require.NoError(t, err)
	// Should have captured the streamed lines.
	assert.NotEmpty(t, respBytes)
}

func TestResponseHashComputation(t *testing.T) {
	responseJSON := `{"id":"test","choices":[{"message":{"content":"hello"},"logprobs":{"content":[{"token":"hello","logprob":-0.1,"top_logprobs":[{"token":"hello","logprob":-0.1}]}]}}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`

	resp, err := completionapi.NewCompletionResponseFromBytes([]byte(responseJSON))
	require.NoError(t, err)

	bodyBytes, err := resp.GetBodyBytes()
	require.NoError(t, err)

	hash := sha256.Sum256(bodyBytes)
	assert.Len(t, hash, 32)

	usage, err := resp.GetUsage()
	require.NoError(t, err)
	assert.Equal(t, uint64(10), usage.PromptTokens)
	assert.Equal(t, uint64(5), usage.CompletionTokens)
}

func TestProcessExecutionHTTPResponse_PartialSSEAfterInterruption(t *testing.T) {
	inferenceID := "devshard-escrow-1-7"
	body := []byte(`data: {"id":"upstream","model":"llama","choices":[{"delta":{"content":"hello"},"logprobs":{"content":[{"token":"hello","logprob":-0.1,"top_logprobs":[{"token":"hello","logprob":-0.1}]}]}}],"usage":{"prompt_tokens":12,"completion_tokens":1}}` + "\n")
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       &interruptedBody{data: body},
	}

	processed, err := ProcessExecutionHTTPResponse(devshardpkg.ExecuteRequest{}, resp, inferenceID)
	require.NoError(t, err)
	require.NotNil(t, processed)
	require.Equal(t, uint64(12), processed.InputTokens)
	require.Equal(t, uint64(1), processed.OutputTokens)
	require.Contains(t, string(processed.ResponseBody), inferenceID)

	expectedHash := sha256.Sum256(processed.ResponseBody)
	require.Equal(t, expectedHash[:], processed.ResponseHash)
}

func TestFetchPayloadsHTTPWithTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := &http.Client{Timeout: time.Minute}
	start := time.Now()
	_, err := fetchPayloadsHTTPWithTimeout(
		context.Background(),
		client,
		20*time.Millisecond,
		server.URL,
		"validator",
		time.Now().UnixNano(),
		1,
		"signature",
	)
	require.Error(t, err)
	require.Less(t, time.Since(start), 500*time.Millisecond)
}

func TestCanonicalizePrompt(t *testing.T) {
	body := []byte(`{"model":"test","seed":42,"logprobs":true}`)
	canonicalized, err := utils.CanonicalizeJSON(body)
	require.NoError(t, err)

	var result map[string]interface{}
	err = json.Unmarshal([]byte(canonicalized), &result)
	require.NoError(t, err)
	assert.Contains(t, result, "model")
	assert.Contains(t, result, "seed")
	assert.Contains(t, result, "logprobs")
}

func TestModifyRequestBody(t *testing.T) {
	body := []byte(`{"model":"test-model","messages":[{"role":"user","content":"hi"}]}`)
	modified, err := completionapi.ModifyRequestBody(body, 42)
	require.NoError(t, err)

	var result map[string]interface{}
	err = json.Unmarshal(modified.NewBody, &result)
	require.NoError(t, err)
	assert.Equal(t, true, result["logprobs"])
	assert.Equal(t, float64(5), result["top_logprobs"])
	assert.Equal(t, float64(42), result["seed"])
	assert.Equal(t, false, result["skip_special_tokens"])
}

func TestDevshardPayloadKey(t *testing.T) {
	key := DevshardPayloadKey("escrow-123", 456)
	assert.Equal(t, "devshard:escrow-123:456", key)
}

func TestDevshardPayloadKey_DifferentEscrows(t *testing.T) {
	key1 := DevshardPayloadKey("escrow-1", 100)
	key2 := DevshardPayloadKey("escrow-2", 100)

	assert.NotEqual(t, key1, key2, "same inference ID in different escrows should have different keys")
	assert.Equal(t, "devshard:escrow-1:100", key1)
	assert.Equal(t, "devshard:escrow-2:100", key2)
}
