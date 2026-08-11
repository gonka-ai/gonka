package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// twoStageNormalize mimics production: the gateway normalizes the client body
// (force-rewriting stream / stream_options / logprobs / top_logprobs on the
// wire) and forwards THAT body to the runtime proxy, which normalizes again.
func twoStageNormalize(t *testing.T, clientBody string) (gatewayIntent clientRequestIntent, proxyReq chatRequest, wire []byte) {
	t.Helper()

	gwBody, gwReq, err := normalizeChatRequest([]byte(clientBody))
	require.NoError(t, err)
	gatewayIntent = clientRequestIntent{
		stream:  streamClientIntentFromRequest(gwReq),
		logprob: logprobClientIntentFromRequest(gwReq),
	}

	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(string(gwBody)))
	proxyBody, proxyReq, err := prepareChatRequestBodyWithTokenLimits(r, defaultOutputTokenLimits(), "")
	require.NoError(t, err)
	return gatewayIntent, proxyReq, proxyBody
}

// The forwarded body cannot answer "what did the client ask for?" — this pins
// why the pinned-intent handoff exists, so a future refactor that drops it fails
// here rather than in production.
func TestClientIntentHandoff_ForwardedBodyReportsForcedShape(t *testing.T) {
	withForceUpstreamStreaming(t, true)

	_, proxyReq, wire := twoStageNormalize(t, `{"messages":[{"role":"user","content":"hi"}],"stream":false}`)

	var raw map[string]any
	require.NoError(t, json.Unmarshal(wire, &raw))
	require.Equal(t, true, raw["stream"], "gateway forces stream on the wire")
	require.Equal(t, true, raw["logprobs"], "gateway forces logprobs on the wire")

	require.True(t, proxyReq.Stream, "body-derived stream is the forced value")
	require.True(t, logprobClientIntentFromRequest(proxyReq).keepLogprobs, "body-derived logprobs is the forced value")
}

func TestClientIntentHandoff_PinnedIntentSurvivesDoubleNormalize(t *testing.T) {
	withForceUpstreamStreaming(t, true)

	gatewayIntent, proxyReq, _ := twoStageNormalize(t, `{"messages":[{"role":"user","content":"hi"}],"stream":false}`)
	require.False(t, gatewayIntent.stream.wantsStream)
	require.False(t, gatewayIntent.logprob.keepLogprobs)

	ctx := withClientRequestIntent(context.Background(), gatewayIntent)
	resolved, source := resolveClientRequestIntent(ctx, proxyReq)

	require.Equal(t, "gateway", source)
	require.False(t, resolved.stream.wantsStream, "a stream:false client must not be routed to handleStreaming")
	require.False(t, resolved.stream.wantsUsage, "forced include_usage must not look like a client ask")
	require.False(t, resolved.logprob.keepLogprobs, "forced logprobs must still be stripped client-facing")
	require.False(t, resolved.logprob.keepTopLogprobs)
}

// Same hazard without the force flag: the gateway forces logprobs unconditionally.
func TestClientIntentHandoff_PinnedIntentProtectsLogprobsWithForceOff(t *testing.T) {
	withForceUpstreamStreaming(t, false)

	gatewayIntent, proxyReq, _ := twoStageNormalize(t, `{"messages":[{"role":"user","content":"hi"}],"stream":true}`)
	require.False(t, gatewayIntent.logprob.keepLogprobs)
	require.True(t, logprobClientIntentFromRequest(proxyReq).keepLogprobs, "body still reports the forced value")

	resolved, _ := resolveClientRequestIntent(withClientRequestIntent(context.Background(), gatewayIntent), proxyReq)
	require.False(t, resolved.logprob.keepLogprobs)
	require.True(t, resolved.stream.wantsStream, "the client did ask for streaming")
}

// A client that genuinely asks for streaming, usage, and logprobs still gets them.
func TestClientIntentHandoff_PreservesGenuineClientAsk(t *testing.T) {
	withForceUpstreamStreaming(t, true)

	gatewayIntent, proxyReq, _ := twoStageNormalize(t, `{
		"messages":[{"role":"user","content":"hi"}],
		"stream":true,
		"stream_options":{"include_usage":true},
		"logprobs":true,
		"top_logprobs":3
	}`)

	resolved, _ := resolveClientRequestIntent(withClientRequestIntent(context.Background(), gatewayIntent), proxyReq)
	require.True(t, resolved.stream.wantsStream)
	require.True(t, resolved.stream.wantsUsage)
	require.True(t, resolved.logprob.keepLogprobs)
	require.True(t, resolved.logprob.keepTopLogprobs)
}

// Direct-to-runtime callers (no gateway hop) still derive intent from the body.
func TestClientIntentHandoff_FallsBackToBodyWithoutPin(t *testing.T) {
	withForceUpstreamStreaming(t, false)

	_, req, err := normalizeChatRequest([]byte(`{"messages":[{"role":"user","content":"hi"}],"stream":true}`))
	require.NoError(t, err)

	resolved, source := resolveClientRequestIntent(context.Background(), req)
	require.Equal(t, "body", source)
	require.True(t, resolved.stream.wantsStream)
}

// End to end through the gateway handler: the runtime sees a forced wire body
// but an accurate pinned intent.
func TestGatewayPinsClientIntentForRuntime(t *testing.T) {
	withForceUpstreamStreaming(t, true)

	var (
		gotIntent clientRequestIntent
		gotPinned bool
		gotBody   []byte
	)
	rt := &devshardRuntime{
		id:    "12",
		model: "Qwen/Test",
		handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotIntent, gotPinned = clientRequestIntentFromContext(r.Context())
			body, err := io.ReadAll(r.Body)
			require.NoError(t, err)
			gotBody = body
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":"x","choices":[{"message":{"role":"assistant","content":"hi"}}]}`))
		}),
	}
	g := NewGateway([]*devshardRuntime{rt}, NewGatewayLimiter(0, 0), "Qwen/Test")
	g.settings.ModelLimits = []GatewayModelLimitSettings{{ModelID: "Qwen/Test", AccessMode: string(gatewayAccessModeOpen)}}

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"Qwen/Test","messages":[{"role":"user","content":"hi"}],"stream":false}`))
	g.handlePooledChat(httptest.NewRecorder(), req)

	require.True(t, gotPinned, "gateway must pin the client's intent for the runtime proxy")
	require.False(t, gotIntent.stream.wantsStream)
	require.False(t, gotIntent.stream.wantsUsage)
	require.False(t, gotIntent.logprob.keepLogprobs)

	var raw map[string]any
	require.NoError(t, json.Unmarshal(gotBody, &raw))
	require.Equal(t, true, raw["stream"], "sanity: the forwarded body really is the forced one")
}
