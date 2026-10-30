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

	entry, reason := capture.cacheEntry("escrow-1", false, "req-source", context.Canceled)

	require.Equal(t, "request_error", reason)
	require.Empty(t, entry.Body)
}

func TestGatewayChatCacheCaptureReportsRequestErrorBeforeEmptyBody(t *testing.T) {
	capture := &gatewayChatCacheCapture{ResponseWriter: httptest.NewRecorder()}

	_, reason := capture.cacheEntry("escrow-1", true, "req-source", context.Canceled)

	require.Equal(t, "request_error", reason)
}

func TestGatewayChatCacheCaptureAllowsSuccessfulResponse(t *testing.T) {
	rec := httptest.NewRecorder()
	capture := &gatewayChatCacheCapture{ResponseWriter: rec}
	writeJSONPayload(capture, http.StatusOK, []byte(`{"choices":[{"index":0,"message":{"content":"ok"},"finish_reason":"stop"}]}`))

	entry, reason := capture.cacheEntry("escrow-1", false, "req-source", nil)

	require.Empty(t, reason)
	require.Equal(t, http.StatusOK, entry.StatusCode)
	require.JSONEq(t, `{"choices":[{"index":0,"message":{"content":"ok"},"finish_reason":"stop"}]}`, string(entry.Body))
}

func TestGatewayChatCacheCaptureAllowsMultiChoiceNonStreamingResponse(t *testing.T) {
	capture := &gatewayChatCacheCapture{ResponseWriter: httptest.NewRecorder()}
	writeJSONPayload(capture, http.StatusOK, []byte(`{"choices":[{"index":0,"message":{"content":"a"},"finish_reason":"stop"},{"index":1,"message":{"content":"b"},"finish_reason":"length"}]}`))

	_, reason := capture.cacheEntry("escrow-1", false, "req-source", nil)

	require.Empty(t, reason)
}

func TestGatewayChatCacheCaptureRejectsIncompleteNonStreamingResponse(t *testing.T) {
	tests := map[string]struct{ body, reason string }{
		"aggregated truncation": {`{"id":"cmpl-1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"partial"},"finish_reason":null}]}`, "incomplete"},
		"no choices":            {`{"id":"cmpl-1","object":"chat.completion","choices":[]}`, "no_choices"},
		"second choice open":    {`{"choices":[{"index":0,"message":{"content":"a"},"finish_reason":"stop"},{"index":1,"message":{"content":"b"},"finish_reason":null}]}`, "incomplete"},
		"empty finish_reason":   {`{"choices":[{"index":0,"message":{"content":"a"},"finish_reason":""}]}`, "incomplete"},
		"boolean finish_reason": {`{"choices":[{"index":0,"message":{"content":"a"},"finish_reason":false}]}`, "incomplete"},
		"numeric finish_reason": {`{"choices":[{"index":0,"message":{"content":"a"},"finish_reason":0}]}`, "incomplete"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			capture := &gatewayChatCacheCapture{ResponseWriter: rec}
			writeJSONPayload(capture, http.StatusOK, []byte(tt.body))

			entry, reason := capture.cacheEntry("escrow-1", false, "req-source", nil)

			require.Equal(t, tt.reason, reason)
			require.Empty(t, entry.Body)
		})
	}
}

// vLLM sends content, the terminal chunk, and usage as separate events; the
// fixtures keep that shape so completion has to be carried across events.
const (
	sseContentChunk  = `data: {"choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":null}]}` + "\n\n"
	sseTerminalChunk = `data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n\n"
	sseUsageChunk    = `data: {"choices":[],"usage":{"completion_tokens":1}}` + "\n\n"
	sseDone          = "data: [DONE]\n\n"
)

func TestGatewayChatCacheCaptureAllowsCompleteStreamingResponse(t *testing.T) {
	tests := map[string]string{
		"vllm shape":           sseContentChunk + sseTerminalChunk + sseUsageChunk + sseDone,
		"stop_reason only":     sseContentChunk + `data: {"choices":[{"index":0,"delta":{},"finish_reason":null,"stop_reason":128009}]}` + "\n\n" + sseDone,
		"string index":         `data: {"choices":[{"index":"0","delta":{"content":"ok"},"finish_reason":null}]}` + "\n\n" + `data: {"choices":[{"index":"0","delta":{},"finish_reason":"stop"}]}` + "\n\n" + sseDone,
		"null after finish":    sseContentChunk + sseTerminalChunk + `data: {"choices":[{"index":0,"delta":{},"finish_reason":null}]}` + "\n\n" + sseDone,
		"multi-line event":     sseContentChunk + `data: {"choices":[{"index":0,` + "\n" + `data: "delta":{},"finish_reason":"stop"}]}` + "\n\n" + sseDone,
		"non-finite logprob":   sseContentChunk + `data: {"choices":[{"index":0,"delta":{},"logprobs":{"content":[{"token":"x","logprob":-Infinity}]},"finish_reason":"stop"}]}` + "\n\n" + sseDone,
		"deterministic error":  `data: {"error":{"message":"bad response_format schema","type":"BadRequestError","code":400}}` + "\n\n" + sseDone,
		"two choices finished": `data: {"choices":[{"index":0,"delta":{"content":"a"},"finish_reason":null},{"index":1,"delta":{"content":"b"},"finish_reason":null}]}` + "\n\n" + `data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"},{"index":1,"delta":{},"finish_reason":"length"}]}` + "\n\n" + sseDone,
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			capture := &gatewayChatCacheCapture{ResponseWriter: rec}
			_, err := capture.Write([]byte(body))
			require.NoError(t, err)

			entry, reason := capture.cacheEntry("escrow-1", true, "req-source", nil)

			require.Empty(t, reason)
			require.True(t, entry.Stream)
		})
	}
}

func TestGatewayChatCacheCaptureRejectsIncompleteStreamingResponse(t *testing.T) {
	tests := map[string]struct{ body, reason string }{
		"partial without done":       {sseContentChunk, "incomplete"},
		"synthetic done":             {sseContentChunk + sseDone, "incomplete"},
		"finish without done":        {sseContentChunk + sseTerminalChunk, "incomplete"},
		"done only":                  {sseDone, "no_choices"},
		"second choice open":         {sseContentChunk + `data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"},{"index":1,"delta":{"content":"b"},"finish_reason":null}]}` + "\n\n" + sseDone, "incomplete"},
		"empty finish_reason":        {sseContentChunk + `data: {"choices":[{"index":0,"delta":{},"finish_reason":""}]}` + "\n\n" + sseDone, "incomplete"},
		"boolean stop_reason":        {sseContentChunk + `data: {"choices":[{"index":0,"delta":{},"finish_reason":null,"stop_reason":false}]}` + "\n\n" + sseDone, "incomplete"},
		"partial then winner error":  {sseContentChunk + `data: {"error":{"message":"inference: winner inference incomplete (nonce_finished=false)"}}` + "\n\n", "incomplete"},
		"partial then host error":    {sseContentChunk + `data: {"error":{"message":"bad response_format schema","type":"BadRequestError","code":400}}` + "\n\n" + sseDone, "incomplete"},
		"partial then empty stream":  {sseContentChunk + `data: {"error":{"message":"empty content stream"}}` + "\n\n" + sseDone, "incomplete"},
		"partial then upstream time": {sseContentChunk + `data: {"error":{"message":"upstream timeout"}}` + "\n\n", "transient_error"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			capture := &gatewayChatCacheCapture{ResponseWriter: rec}
			_, err := capture.Write([]byte(tt.body))
			require.NoError(t, err)

			entry, reason := capture.cacheEntry("escrow-1", true, "req-source", nil)

			require.Equal(t, tt.reason, reason)
			require.Empty(t, entry.Body)
		})
	}
}

func TestGatewayChatCacheCaptureAllowsDeterministicOpenAIStyleBadRequest(t *testing.T) {
	rec := httptest.NewRecorder()
	capture := &gatewayChatCacheCapture{ResponseWriter: rec}
	writeJSONPayload(capture, http.StatusBadRequest, []byte(`{"error":{"message":"bad response_format schema","type":"BadRequestError","code":400}}`))

	entry, reason := capture.cacheEntry("escrow-1", false, "req-source", nil)

	require.Empty(t, reason)
	require.Equal(t, http.StatusBadRequest, entry.StatusCode)
	require.JSONEq(t, `{"error":{"message":"bad response_format schema","type":"BadRequestError","code":400}}`, string(entry.Body))
}

func TestGatewayChatCacheCaptureRejectsRuntimeAndCapabilityErrors(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		reason string
	}{
		{
			name:   "context canceled",
			status: http.StatusBadGateway,
			body:   `{"error":{"message":"context canceled"}}`,
			reason: "transient_error",
		},
		{
			name:   "rate limited",
			status: http.StatusTooManyRequests,
			body:   `{"error":{"message":"rate limit exceeded","type":"RateLimitError","code":429}}`,
			reason: "transient_error",
		},
		{
			name:   "unsupported model",
			status: http.StatusBadRequest,
			body:   `{"error":{"message":"unsupported model \"Nope/Model\"","type":"BadRequestError","code":400}}`,
			reason: "transient_error",
		},
		{
			name:   "context length",
			status: http.StatusBadRequest,
			body:   `{"error":{"message":"This model's maximum context length is 131072 tokens. However, you requested 150000 tokens.","type":"BadRequestError","code":400}}`,
			reason: "transient_error",
		},
		{
			name:   "server error",
			status: http.StatusInternalServerError,
			body:   `{"error":{"message":"internal server error","type":"InternalServerError","code":500}}`,
			reason: "transient_error",
		},
		{
			name:   "unexpected status",
			status: http.StatusBadGateway,
			body:   `{"error":{"message":"bad response_format schema","type":"BadRequestError","code":400}}`,
			reason: "status",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			capture := &gatewayChatCacheCapture{ResponseWriter: rec}
			writeJSONPayload(capture, tt.status, []byte(tt.body))

			entry, reason := capture.cacheEntry("escrow-1", false, "req-source", nil)

			require.Equal(t, tt.reason, reason)
			require.Empty(t, entry.Body)
		})
	}
}

func okBody(marker byte, size int) []byte {
	filler := make([]byte, size)
	for i := range filler {
		filler[i] = 'a' + (marker+byte(i))%26
	}
	return []byte(`{"choices":[{"index":0,"message":{"content":"` + string(filler) + `"},"finish_reason":"stop"}]}`)
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
