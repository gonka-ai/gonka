package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizeChatRequest_DecodesStreamOptionsIncludeUsage(t *testing.T) {
	_, req, err := normalizeChatRequest([]byte(`{
		"messages":[{"role":"user","content":"hi"}],
		"stream":true,
		"stream_options":{"include_usage":true}
	}`))
	require.NoError(t, err)
	require.True(t, req.Stream)
	require.True(t, req.StreamOptions.IncludeUsage)

	intent := streamClientIntentFromRequest(req)
	require.True(t, intent.wantsStream)
	require.True(t, intent.wantsUsage)
}

func TestNormalizeChatRequest_StreamOptionsFalseIncludeUsage(t *testing.T) {
	_, req, err := normalizeChatRequest([]byte(`{
		"messages":[{"role":"user","content":"hi"}],
		"stream":true,
		"stream_options":{"include_usage":false}
	}`))
	require.NoError(t, err)
	require.True(t, req.Stream)
	require.False(t, req.StreamOptions.IncludeUsage)
	intent := streamClientIntentFromRequest(req)
	require.True(t, intent.wantsStream)
	require.False(t, intent.wantsUsage)
}

func TestNormalizeChatRequest_StreamFalseClearsUsageIntent(t *testing.T) {
	_, req, err := normalizeChatRequest([]byte(`{
		"messages":[{"role":"user","content":"hi"}],
		"stream":false,
		"stream_options":{"include_usage":true}
	}`))
	require.NoError(t, err)
	require.False(t, req.Stream)
	require.False(t, req.StreamOptions.IncludeUsage, "PreValidation strips stream_options when stream is false")
	intent := streamClientIntentFromRequest(req)
	require.False(t, intent.wantsStream)
	require.False(t, intent.wantsUsage)
}

func TestStreamClientIntentContextRoundTrip(t *testing.T) {
	intent := streamClientIntent{wantsStream: true, wantsUsage: true}
	ctx := withStreamClientIntent(context.Background(), intent)
	got := streamClientIntentFromContext(ctx)
	require.Equal(t, intent, got)
	require.Equal(t, streamClientIntent{}, streamClientIntentFromContext(context.Background()))
}

func TestHandleChatCompletions_BranchesOnStreamClientIntent(t *testing.T) {
	zeroReceiptTimeout(t)
	env := setupTestProxy(t, 3, nil, true)

	t.Run("stream false aggregates to chat.completion JSON", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
			bytes.NewBufferString(`{"messages":[{"role":"user","content":"hello"}],"stream":false}`))
		rec := httptest.NewRecorder()
		env.proxy.handleChatCompletions(rec, req)
		require.Equal(t, http.StatusOK, rec.Code)
		require.Equal(t, "application/json", rec.Header().Get("Content-Type"))
		var body map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
		require.Equal(t, "chat.completion", body["object"])
		msg := body["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)
		require.Equal(t, "stub", msg["content"])
	})

	t.Run("stream true keeps SSE shape", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
			bytes.NewBufferString(`{"messages":[{"role":"user","content":"hello"}],"stream":true,"stream_options":{"include_usage":true}}`))
		rec := httptest.NewRecorder()
		env.proxy.handleChatCompletions(rec, req)
		require.Equal(t, http.StatusOK, rec.Code)
		ct := rec.Header().Get("Content-Type")
		require.True(t, strings.Contains(ct, "text/event-stream") || strings.Contains(rec.Body.String(), "data:"),
			"streaming client must receive SSE, content-type=%q body=%q", ct, rec.Body.String())
		require.Contains(t, rec.Body.String(), "data:")
		require.NotContains(t, rec.Body.String(), `"object":"chat.completion"`)
	})
}
