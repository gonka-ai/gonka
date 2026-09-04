package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPooledForwardingRecordsTheAffinityKeyForTheProxy(t *testing.T) {
	t.Parallel()
	var forwardedBody string
	var forwardedKey string
	var keyRecorded bool

	runtime := &devshardRuntime{
		id:    "12",
		model: "Qwen/Test",
		handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			forwardedKey, keyRecorded = affinityKeyFromContext(r.Context())
			body, _ := io.ReadAll(r.Body)
			forwardedBody = string(body)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
		}),
	}
	gateway := NewGateway([]*devshardRuntime{runtime}, NewGatewayLimiter(0, 0), "Qwen/Test")
	gateway.settings.ModelLimits = []GatewayModelLimitSettings{{ModelID: "Qwen/Test", AccessMode: string(gatewayAccessModeOpen)}}

	recorder := httptest.NewRecorder()
	gateway.handlePooledChat(recorder, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"Qwen/Test","prompt_cache_key":"conversation-alpha","messages":[{"role":"user","content":"hello"}]}`)))

	require.Equal(t, http.StatusOK, recorder.Code)
	require.NotContains(t, forwardedBody, "prompt_cache_key",
		"the field is still stripped from the wire body")
	require.True(t, keyRecorded, "the proxy has no other source for the client's cache key")
	require.Equal(t, "conversation-alpha", forwardedKey)
}
