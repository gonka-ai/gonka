package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"devshard/testenv/mockopenai"

	"github.com/stretchr/testify/require"
)

func withForceUpstreamStreaming(t *testing.T, on bool) {
	t.Helper()
	prev := ForceUpstreamStreamingEnabled()
	setForceUpstreamStreaming(on)
	t.Cleanup(func() { setForceUpstreamStreaming(prev) })
}

func TestChatCacheKey_IsolatesClientStreamShape(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"hi"}],"stream":true,"max_tokens":64}`)
	keys := map[string]string{}
	for _, intent := range []streamClientIntent{
		{},
		{wantsStream: true},
		{wantsStream: true, wantsUsage: true},
		{wantsUsage: true}, // not produced by real requests; still must hash distinctly
	} {
		key := chatCacheKey("m", body, intent)
		label := fmt.Sprintf("stream=%v usage=%v", intent.wantsStream, intent.wantsUsage)
		for other, otherKey := range keys {
			require.NotEqual(t, otherKey, key, "cache keys must differ: %s vs %s", other, label)
		}
		keys[label] = key
		require.Equal(t, chatCacheKey("m", body, intent), key)
	}
}

func TestNormalizeChatRequest_ForceUpstreamStreamingRewritesBodyKeepsClientIntent(t *testing.T) {
	withForceUpstreamStreaming(t, true)

	body, req, err := normalizeChatRequest([]byte(`{
		"messages":[{"role":"user","content":"hi"}],
		"stream":false
	}`))
	require.NoError(t, err)
	require.False(t, req.Stream, "chatRequest.Stream must keep the client ask")
	require.False(t, req.StreamOptions.IncludeUsage)

	var raw map[string]any
	require.NoError(t, json.Unmarshal(body, &raw))
	require.Equal(t, true, raw["stream"], "wire body must force stream:true")
	so, ok := raw["stream_options"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, true, so["include_usage"])

	intent := streamClientIntentFromRequest(req)
	require.False(t, intent.wantsStream)
	require.False(t, intent.wantsUsage)
}

func TestNormalizeChatRequest_ForceUpstreamStreamingOffIsPassthrough(t *testing.T) {
	withForceUpstreamStreaming(t, false)

	body, req, err := normalizeChatRequest([]byte(`{
		"messages":[{"role":"user","content":"hi"}],
		"stream":false
	}`))
	require.NoError(t, err)
	require.False(t, req.Stream)

	var raw map[string]any
	require.NoError(t, json.Unmarshal(body, &raw))
	require.Equal(t, false, raw["stream"])
	require.NotContains(t, raw, "stream_options")
}

func TestNormalizeChatRequest_ForceUpstreamStreamingPreservesClientUsageIntent(t *testing.T) {
	withForceUpstreamStreaming(t, true)

	_, req, err := normalizeChatRequest([]byte(`{
		"messages":[{"role":"user","content":"hi"}],
		"stream":true,
		"stream_options":{"include_usage":false}
	}`))
	require.NoError(t, err)
	require.True(t, req.Stream)
	require.False(t, req.StreamOptions.IncludeUsage)
	require.False(t, streamClientIntentFromRequest(req).wantsUsage)
}

func TestMockOpenAI_ForceUpstreamAggregatedMatchesNonStreamJSON(t *testing.T) {
	// Differential: mock saw stream:true (forced shape) → SSE → aggregateSSEStream
	// should match the mock's own non-streaming JSON content for the same seed.
	srv := httptest.NewServer(mockopenai.NewServer(mockopenai.Config{
		Faults: mockopenai.FaultConfig{StreamChunkDelay: time.Millisecond},
	}).Handler())
	t.Cleanup(srv.Close)

	prompt := `{"model":"test-model","messages":[{"role":"user","content":"fixed-seed-hello"}],"stream":false,"max_tokens":32}`

	jsonResp := postMockChat(t, srv.URL, prompt)
	require.Equal(t, "chat.completion", jsonResp["object"])
	jsonChoice := jsonResp["choices"].([]any)[0].(map[string]any)
	jsonMsg := jsonChoice["message"].(map[string]any)

	var streamReq map[string]any
	require.NoError(t, json.Unmarshal([]byte(prompt), &streamReq))
	streamReq["stream"] = true
	streamReq["stream_options"] = map[string]any{"include_usage": true}
	streamBody, err := json.Marshal(streamReq)
	require.NoError(t, err)

	sse := postMockChatRaw(t, srv.URL, streamBody)
	require.Contains(t, sse, "data:")
	aggregated := aggregateSSEStream([]byte(sse), logprobClientIntent{})
	var agg map[string]any
	require.NoError(t, json.Unmarshal(aggregated, &agg))
	require.Equal(t, "chat.completion", agg["object"])
	aggChoice := agg["choices"].([]any)[0].(map[string]any)
	aggMsg := aggChoice["message"].(map[string]any)
	require.Equal(t, jsonMsg["content"], aggMsg["content"])
	require.Equal(t, jsonMsg["role"], aggMsg["role"])
	require.Equal(t, jsonChoice["finish_reason"], aggChoice["finish_reason"])
	require.Equal(t, jsonResp["model"], agg["model"])
}

func TestMockOpenAI_ReceivesStreamingWhenClientAskedNonStream(t *testing.T) {
	withForceUpstreamStreaming(t, true)

	var sawUpstreamStream bool
	inner := mockopenai.NewServer(mockopenai.DefaultConfig()).Handler()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		var req struct {
			Stream bool `json:"stream"`
		}
		require.NoError(t, json.Unmarshal(body, &req))
		sawUpstreamStream = req.Stream
		r.Body = io.NopCloser(strings.NewReader(string(body)))
		inner.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)

	// Normalize as the gateway would before sending upstream.
	forcedBody, req, err := normalizeChatRequest([]byte(`{
		"messages":[{"role":"user","content":"hi"}],
		"stream":false
	}`))
	require.NoError(t, err)
	require.False(t, req.Stream)

	raw := postMockChatRaw(t, srv.URL, forcedBody)
	require.True(t, sawUpstreamStream, "mock must see stream:true on the wire")
	require.Contains(t, raw, "data:")

	aggregated := aggregateSSEStream([]byte(raw), logprobClientIntent{})
	var agg map[string]any
	require.NoError(t, json.Unmarshal(aggregated, &agg))
	require.Equal(t, "chat.completion", agg["object"])
	require.NotEmpty(t, agg["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)["content"])
}

func TestNormalizeChatRequest_ForceUpstreamStreamingNeverStreamWithoutUsage(t *testing.T) {
	withForceUpstreamStreaming(t, true)
	for i := 0; i < 50; i++ {
		body, _, err := normalizeChatRequest([]byte(`{
			"messages":[{"role":"user","content":"hi"}],
			"stream":false
		}`))
		require.NoError(t, err)
		var raw map[string]any
		require.NoError(t, json.Unmarshal(body, &raw))
		require.Equal(t, true, raw["stream"], "iteration %d", i)
		so, ok := raw["stream_options"].(map[string]any)
		require.True(t, ok, "iteration %d: stream:true must carry stream_options", i)
		require.Equal(t, true, so["include_usage"], "iteration %d", i)
	}
}

func TestNormalizeChatRequest_ForceUpstreamStreamingSnapshotIgnoresMidFlightFlip(t *testing.T) {
	// Snapshot at context creation must drive both PostLimits rules even if the
	// process-wide flag flips before PostLimits runs.
	withForceUpstreamStreaming(t, true)
	ctx, err := newRequestFilterContext([]byte(`{
		"messages":[{"role":"user","content":"hi"}],
		"stream":false
	}`), false, defaultOutputTokenLimits())
	require.NoError(t, err)
	require.True(t, ctx.ForceUpstreamStreaming)

	setForceUpstreamStreaming(false)
	require.False(t, ForceUpstreamStreamingEnabled())
	require.True(t, ctx.ForceUpstreamStreaming, "request snapshot must stay true")

	require.NoError(t, defaultParameterCatalog.Apply(RequestFilterStagePostLimits, ctx))

	stream, ok := ctx.Document.Get("stream")
	require.True(t, ok)
	require.Equal(t, true, stream)
	so, ok := ctx.Document.Object("stream_options")
	require.True(t, ok, "stream:true without include_usage must not occur after a mid-flight flip")
	require.Equal(t, true, so["include_usage"])
}

func TestNormalizeChatRequest_ForceUpstreamStreamingRaceToggle(t *testing.T) {
	withForceUpstreamStreaming(t, false)

	var wg sync.WaitGroup
	stop := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				setForceUpstreamStreaming(true)
				setForceUpstreamStreaming(false)
			}
		}
	}()

	for i := 0; i < 200; i++ {
		body, _, err := normalizeChatRequest([]byte(`{
			"messages":[{"role":"user","content":"hi"}],
			"stream":false
		}`))
		require.NoError(t, err)
		var raw map[string]any
		require.NoError(t, json.Unmarshal(body, &raw))
		stream, _ := raw["stream"].(bool)
		if stream {
			so, ok := raw["stream_options"].(map[string]any)
			require.True(t, ok, "iteration %d: forced stream must include stream_options", i)
			require.Equal(t, true, so["include_usage"], "iteration %d", i)
		} else {
			require.NotContains(t, raw, "stream_options", "iteration %d: unforced body must not gain stream_options alone", i)
		}
	}
	close(stop)
	wg.Wait()
}

func postMockChat(t *testing.T, baseURL, body string) map[string]any {
	t.Helper()
	raw := postMockChatRaw(t, baseURL, []byte(body))
	var resp map[string]any
	require.NoError(t, json.Unmarshal([]byte(raw), &resp))
	return resp
}

func postMockChatRaw(t *testing.T, baseURL string, body []byte) string {
	t.Helper()
	httpReq, err := http.NewRequest(http.MethodPost, baseURL+"/v1/chat/completions", strings.NewReader(string(body)))
	require.NoError(t, err)
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(httpReq)
	require.NoError(t, err)
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	return string(raw)
}
