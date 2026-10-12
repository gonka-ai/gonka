package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The pooled layer normalizes the body before forwarding it, and normalization force-enables the very
// fields the strip is meant to remove. Once that happens the body no longer records what the client
// asked for, so the pooled layer is the last place that can tell the two apart.
func TestPooledForwardingCarriesTheClientsIntentNotTheForcedBody(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		clientBody string
		want       clientResponseIntent
	}{
		{
			name:       "the client asked for nothing",
			clientBody: `{"model":"Qwen/Test","messages":[{"role":"user","content":"hello"}]}`,
		},
		{
			name:       "the client asked for logprobs alone",
			clientBody: `{"model":"Qwen/Test","logprobs":true,"messages":[{"role":"user","content":"hello"}]}`,
			want:       clientResponseIntent{keepLogprobs: true},
		},
		{
			name:       "the client asked for alternatives too",
			clientBody: `{"model":"Qwen/Test","logprobs":true,"top_logprobs":2,"messages":[{"role":"user","content":"hello"}]}`,
			want:       clientResponseIntent{keepLogprobs: true, keepTopLogprobs: true},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			var forwardedBody string
			var forwardedIntent clientResponseIntent
			var intentRecorded bool

			runtime := &devshardRuntime{
				id:    "12",
				model: "Qwen/Test",
				handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					forwardedIntent, intentRecorded = clientResponseIntentFromContext(r.Context())
					body := make([]byte, r.ContentLength)
					_, _ = r.Body.Read(body)
					forwardedBody = string(body)
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
				}),
			}
			gateway := NewGateway([]*devshardRuntime{runtime}, NewGatewayLimiter(0, 0), "Qwen/Test")
			gateway.settings.ModelLimits = []GatewayModelLimitSettings{{ModelID: "Qwen/Test", AccessMode: string(gatewayAccessModeOpen)}}

			recorder := httptest.NewRecorder()
			gateway.handlePooledChat(recorder, httptest.NewRequest(
				http.MethodPost, "/v1/chat/completions", strings.NewReader(testCase.clientBody)))

			require.Equal(t, http.StatusOK, recorder.Code)
			require.Contains(t, forwardedBody, `"logprobs":true`,
				"the gateway forces logprobs upstream, so the forwarded body cannot report client intent")
			require.True(t, intentRecorded, "the proxy has no other source for the client's intent")
			require.Equal(t, testCase.want, forwardedIntent)
		})
	}
}

// The proxy sees a normalized body whether it was reached through the pooled layer or directly, so a
// recorded intent has to outrank it. Reading the body instead hands back the gateway's own forcing.
func TestARecordedIntentOutranksTheNormalizedBody(t *testing.T) {
	t.Parallel()
	forwarded, _, err := normalizeChatRequestForAuthAndLimits(
		[]byte(`{"messages":[{"role":"user","content":"hello"}]}`), false, defaultOutputTokenLimits(), "llama")
	require.NoError(t, err)
	require.Contains(t, string(forwarded), `"logprobs":true`, "the fixture must carry the forced fields")

	var normalized chatRequest
	require.NoError(t, json.Unmarshal(forwarded, &normalized))
	require.True(t, normalized.Logprobs)

	recorded := clientResponseIntent{keepUsage: true}
	got := resolveClientResponseIntent(withClientResponseIntent(context.Background(), recorded), normalized)
	require.Equal(t, recorded, got, "the pooled layer's record must survive the normalized body")

	got = resolveClientResponseIntent(context.Background(), normalized)
	require.Equal(t, clientResponseIntent{keepLogprobs: true, keepTopLogprobs: true}, got,
		"with nothing recorded the body is the only source left")
}

// Normalization erases the client's request from the body, so two clients that differ only in what they
// asked to see produce the same normalized body and would otherwise share one cached response.
func TestTheCacheKeySeparatesClientsWhoAskedForDifferentFields(t *testing.T) {
	t.Parallel()
	body := []byte(`{"model":"Qwen/Test","logprobs":true,"messages":[{"role":"user","content":"hello"}]}`)

	keys := map[string]string{}
	for _, intent := range []clientResponseIntent{
		{},
		{keepLogprobs: true},
		{keepLogprobs: true, keepTopLogprobs: true},
		{keepUsage: true},
	} {
		key := chatCacheKey("Qwen/Test", body, intent)
		if previous, clash := keys[key]; clash {
			t.Fatalf("intent %+v shares a cache key with %s", intent, previous)
		}
		keys[key] = key
	}
}
