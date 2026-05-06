package public

import (
	"crypto/sha256"
	"decentralized-api/completionapi"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestProxyResponse_HashConsistency verifies that ProxyResponse and
// ProcessHTTPResponse produce identical processor output (and therefore
// identical hashes) for the same SSE stream. Before the fix,
// proxyTextStreamResponse fed empty lines to the processor while
// processSSE skipped them, causing different GetBodyBytes and hashes.
func TestProxyResponse_HashConsistency(t *testing.T) {
	// Typical SSE body with empty lines separating events (per SSE spec).
	sseBody := "data: {\"id\":\"1\",\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\ndata: [DONE]\n\n"

	inferenceId := "test-inference"

	// Path 1: ProxyResponse (executor path with ResponseWriter).
	proxyProcessor := completionapi.NewExecutorResponseProcessor(inferenceId)
	proxyResp := &http.Response{
		StatusCode: 200,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(sseBody)),
	}
	recorder := httptest.NewRecorder()
	ProxyResponse(proxyResp, recorder, true, proxyProcessor, inferenceId)

	proxyCompletion, err := proxyProcessor.GetResponse()
	require.NoError(t, err)
	proxyBytes, err := proxyCompletion.GetBodyBytes()
	require.NoError(t, err)

	// Path 2: ProcessHTTPResponse (validator path without ResponseWriter).
	processProcessor := completionapi.NewExecutorResponseProcessor(inferenceId)
	processResp := &http.Response{
		StatusCode: 200,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(sseBody)),
	}
	err = completionapi.ProcessHTTPResponse(processResp, processProcessor)
	require.NoError(t, err)

	processCompletion, err := processProcessor.GetResponse()
	require.NoError(t, err)
	processBytes, err := processCompletion.GetBodyBytes()
	require.NoError(t, err)

	// Both paths must produce identical body bytes and hashes.
	require.Equal(t, string(processBytes), string(proxyBytes),
		"ProxyResponse and ProcessHTTPResponse produced different body bytes")

	proxyHash := sha256.Sum256(proxyBytes)
	processHash := sha256.Sum256(processBytes)
	require.Equal(t, proxyHash, processHash,
		"ProxyResponse and ProcessHTTPResponse produced different hashes")
}
