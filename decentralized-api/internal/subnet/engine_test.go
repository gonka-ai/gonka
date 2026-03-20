package subnet

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"decentralized-api/completionapi"
	"decentralized-api/utils"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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

func TestSubnetPayloadKey(t *testing.T) {
	key := SubnetPayloadKey("escrow-123", 456)
	assert.Equal(t, "subnet:escrow-123:456", key)
}

func TestSubnetPayloadKey_DifferentEscrows(t *testing.T) {
	key1 := SubnetPayloadKey("escrow-1", 100)
	key2 := SubnetPayloadKey("escrow-2", 100)

	assert.NotEqual(t, key1, key2, "same inference ID in different escrows should have different keys")
	assert.Equal(t, "subnet:escrow-1:100", key1)
	assert.Equal(t, "subnet:escrow-2:100", key2)
}
