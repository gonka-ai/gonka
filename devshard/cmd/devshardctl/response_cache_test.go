package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestGatewayChatCacheCaptureRejectsCanceledRequestError(t *testing.T) {
	rec := httptest.NewRecorder()
	capture := &gatewayChatCacheCapture{ResponseWriter: rec}
	writeGatewayJSONError(capture, http.StatusBadGateway, context.Canceled.Error())

	entry, ok := capture.cacheEntry("escrow-1", false, "req-source", context.Canceled)

	require.False(t, ok)
	require.Empty(t, entry.Body)
}

func TestGatewayChatCacheCaptureAllowsSuccessfulResponse(t *testing.T) {
	rec := httptest.NewRecorder()
	capture := &gatewayChatCacheCapture{ResponseWriter: rec}
	writeJSONPayload(capture, http.StatusOK, []byte(`{"choices":[{"message":{"content":"ok"}}]}`))

	entry, ok := capture.cacheEntry("escrow-1", false, "req-source", nil)

	require.True(t, ok)
	require.Equal(t, http.StatusOK, entry.StatusCode)
	require.JSONEq(t, `{"choices":[{"message":{"content":"ok"}}]}`, string(entry.Body))
}

func TestGatewayChatCacheCaptureAllowsDeterministicOpenAIStyleBadRequest(t *testing.T) {
	rec := httptest.NewRecorder()
	capture := &gatewayChatCacheCapture{ResponseWriter: rec}
	writeJSONPayload(capture, http.StatusBadRequest, []byte(`{"error":{"message":"bad response_format schema","type":"BadRequestError","code":400}}`))

	entry, ok := capture.cacheEntry("escrow-1", false, "req-source", nil)

	require.True(t, ok)
	require.Equal(t, http.StatusBadRequest, entry.StatusCode)
	require.JSONEq(t, `{"error":{"message":"bad response_format schema","type":"BadRequestError","code":400}}`, string(entry.Body))
}

func TestGatewayChatCacheCaptureRejectsRuntimeAndCapabilityErrors(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
	}{
		{
			name:   "context canceled",
			status: http.StatusBadGateway,
			body:   `{"error":{"message":"context canceled"}}`,
		},
		{
			name:   "rate limited",
			status: http.StatusTooManyRequests,
			body:   `{"error":{"message":"rate limit exceeded","type":"RateLimitError","code":429}}`,
		},
		{
			name:   "unsupported model",
			status: http.StatusBadRequest,
			body:   `{"error":{"message":"unsupported model \"Nope/Model\"","type":"BadRequestError","code":400}}`,
		},
		{
			name:   "context length",
			status: http.StatusBadRequest,
			body:   `{"error":{"message":"This model's maximum context length is 131072 tokens. However, you requested 150000 tokens.","type":"BadRequestError","code":400}}`,
		},
		{
			name:   "server error",
			status: http.StatusInternalServerError,
			body:   `{"error":{"message":"internal server error","type":"InternalServerError","code":500}}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			capture := &gatewayChatCacheCapture{ResponseWriter: rec}
			writeJSONPayload(capture, tt.status, []byte(tt.body))

			entry, ok := capture.cacheEntry("escrow-1", false, "req-source", nil)

			require.False(t, ok)
			require.Empty(t, entry.Body)
		})
	}
}

func TestChatResponseCacheDropsPreviouslyCachedNonCacheableErrors(t *testing.T) {
	cache := newChatResponseCache(time.Minute)
	cache.entries["bad"] = cachedChatResponse{
		EscrowID:   "escrow-1",
		StatusCode: http.StatusBadGateway,
		Body:       []byte(`{"error":{"message":"context canceled"}}`),
		ExpiresAt:  time.Now().Add(time.Minute),
	}

	entry, ok := cache.Get("bad", time.Now())

	require.False(t, ok)
	require.Empty(t, entry.Body)
}
