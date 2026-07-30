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

func okBody(marker byte, size int) []byte {
	filler := make([]byte, size)
	for i := range filler {
		filler[i] = 'a' + (marker+byte(i))%26
	}
	return []byte(`{"choices":[{"message":{"content":"` + string(filler) + `"}}]}`)
}

func cacheEntryForTest(marker byte, bodySize int) cachedChatResponse {
	return cachedChatResponse{
		EscrowID:   "escrow-1",
		StatusCode: http.StatusOK,
		Body:       okBody(marker, bodySize),
	}
}

func TestChatResponseCacheSweepsExpiredEntriesOnSet(t *testing.T) {
	cache := newChatResponseCache(time.Minute, 0)
	start := time.Now()

	cache.Set("old-1", cacheEntryForTest(1, 10), start)
	cache.Set("old-2", cacheEntryForTest(2, 10), start)

	count, _ := cache.Stats()
	require.Equal(t, 2, count)

	// A Set past both the TTL and the sweep interval must remove the
	// expired entries even though their keys are never looked up again.
	cache.Set("new", cacheEntryForTest(3, 10), start.Add(2*time.Minute))

	count, _ = cache.Stats()
	require.Equal(t, 1, count)
	_, ok := cache.entries["old-1"]
	require.False(t, ok)
	_, ok = cache.entries["old-2"]
	require.False(t, ok)
	_, ok = cache.entries["new"]
	require.True(t, ok)
}

func TestChatResponseCacheEvictsWhenOverByteCap(t *testing.T) {
	// Cap fits roughly two 4KB entries plus overhead, not three.
	cache := newChatResponseCache(time.Minute, 10_000)
	now := time.Now()

	cache.Set("a", cacheEntryForTest(1, 4096), now)
	cache.Set("b", cacheEntryForTest(2, 4096), now)
	cache.Set("c", cacheEntryForTest(3, 4096), now)

	_, totalBytes := cache.Stats()
	require.LessOrEqual(t, totalBytes, int64(10_000))

	// The just-inserted entry must survive eviction.
	_, ok := cache.entries["c"]
	require.True(t, ok)
}

func TestChatResponseCacheOverwriteDoesNotLeakBytes(t *testing.T) {
	cache := newChatResponseCache(time.Minute, 1<<20)
	now := time.Now()

	for i := 0; i < 100; i++ {
		cache.Set("same-key", cacheEntryForTest(byte(i), 4096), now)
	}

	count, totalBytes := cache.Stats()
	require.Equal(t, 1, count)
	require.Less(t, totalBytes, int64(2*4096+2*chatCacheEntryOverhead))

	entry, ok := cache.Get("same-key", now)
	require.True(t, ok)
	require.Equal(t, string(okBody(99, 4096)), string(entry.Body))
}

func TestChatResponseCacheGetDeletesExpiredAndAdjustsBytes(t *testing.T) {
	cache := newChatResponseCache(time.Minute, 0)
	now := time.Now()

	cache.Set("k", cacheEntryForTest(1, 128), now)
	_, totalBytes := cache.Stats()
	require.Greater(t, totalBytes, int64(0))

	_, ok := cache.Get("k", now.Add(2*time.Minute))
	require.False(t, ok)

	count, totalBytes := cache.Stats()
	require.Equal(t, 0, count)
	require.Equal(t, int64(0), totalBytes)
}

func TestChatResponseCacheDropsPreviouslyCachedNonCacheableErrors(t *testing.T) {
	cache := newChatResponseCache(time.Minute, 0)
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
