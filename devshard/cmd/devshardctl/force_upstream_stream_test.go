package main

import (
	"encoding/json"
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

func normalizeUpstreamChatRequest(body []byte) ([]byte, chatRequest, error) {
	return upstreamChatRequestPipeline().Normalize(body, false, defaultOutputTokenLimits(), "")
}

func TestNormalizeChatRequest_ForceUpstreamStreamingRewritesBodyKeepsClientIntent(t *testing.T) {
	withForceUpstreamStreaming(t, true)

	body, req, err := normalizeUpstreamChatRequest([]byte(`{
		"messages":[{"role":"user","content":"hi"}],
		"stream":false
	}`))
	require.NoError(t, err)
	require.False(t, req.Stream, "chatRequest.Stream must keep the client ask")
	require.False(t, req.IncludeUsage)

	var raw map[string]any
	require.NoError(t, json.Unmarshal(body, &raw))
	require.Equal(t, true, raw["stream"], "wire body must force stream:true")
	so, ok := raw["stream_options"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, true, so["include_usage"])
}

func TestNormalizeChatRequest_ForceUpstreamStreamingOffIsPassthrough(t *testing.T) {
	withForceUpstreamStreaming(t, false)

	body, req, err := normalizeUpstreamChatRequest([]byte(`{
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

func TestDefaultPipeline_IgnoresForceUpstreamStreamingFlag(t *testing.T) {
	withForceUpstreamStreaming(t, true)

	body, req, err := defaultChatRequestPipeline().Normalize([]byte(`{
		"messages":[{"role":"user","content":"hi"}],
		"stream":false
	}`), false, defaultOutputTokenLimits(), "")
	require.NoError(t, err)
	require.False(t, req.Stream)

	var raw map[string]any
	require.NoError(t, json.Unmarshal(body, &raw))
	require.Equal(t, false, raw["stream"])
	require.NotContains(t, raw, "stream_options")
}

func TestNormalizeChatRequest_ForceUpstreamStreamingPreservesClientUsageIntent(t *testing.T) {
	withForceUpstreamStreaming(t, true)

	_, req, err := normalizeUpstreamChatRequest([]byte(`{
		"messages":[{"role":"user","content":"hi"}],
		"stream":true,
		"stream_options":{"include_usage":false}
	}`))
	require.NoError(t, err)
	require.True(t, req.Stream)
	require.False(t, req.IncludeUsage)
}

func TestMockOpenAI_ForceUpstreamAggregatedMatchesNonStreamJSON(t *testing.T) {
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
	aggregated := aggregateSSEStream([]byte(sse), clientResponseIntent{})
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

	forcedBody, req, err := normalizeUpstreamChatRequest([]byte(`{
		"messages":[{"role":"user","content":"hi"}],
		"stream":false
	}`))
	require.NoError(t, err)
	require.False(t, req.Stream)

	raw := postMockChatRaw(t, srv.URL, forcedBody)
	require.True(t, sawUpstreamStream, "mock must see stream:true on the wire")
	require.Contains(t, raw, "data:")

	aggregated := aggregateSSEStream([]byte(raw), clientResponseIntent{})
	var agg map[string]any
	require.NoError(t, json.Unmarshal(aggregated, &agg))
	require.Equal(t, "chat.completion", agg["object"])
	require.NotEmpty(t, agg["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)["content"])
}

func TestNormalizeChatRequest_ForceUpstreamStreamingNeverStreamWithoutUsage(t *testing.T) {
	withForceUpstreamStreaming(t, true)
	for i := 0; i < 50; i++ {
		body, _, err := normalizeUpstreamChatRequest([]byte(`{
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

	upstreamChatRequestPipeline().applyForcedStreaming(ctx)
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
		body, _, err := normalizeUpstreamChatRequest([]byte(`{
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

func TestForceUpstreamStreamingFromSettings_NilMeansOn(t *testing.T) {
	require.True(t, forceUpstreamStreamingFromSettings(RedundancySettings{}))
	require.True(t, forceUpstreamStreamingFromSettings(RedundancySettings{ForceUpstreamStreaming: boolPtr(true)}))
	require.False(t, forceUpstreamStreamingFromSettings(RedundancySettings{ForceUpstreamStreaming: boolPtr(false)}))
}

func TestApplyRedundancySettings_SetsForceUpstreamStreamingFlag(t *testing.T) {
	withForceUpstreamStreaming(t, true)
	t.Cleanup(func() { ApplyRedundancySettings(DefaultRedundancySettings()) })
	settings := DefaultRedundancySettings()
	settings.ForceUpstreamStreaming = boolPtr(false)
	ApplyRedundancySettings(settings)
	require.False(t, ForceUpstreamStreamingEnabled())

	settings.ForceUpstreamStreaming = boolPtr(true)
	ApplyRedundancySettings(settings)
	require.True(t, ForceUpstreamStreamingEnabled())
}

func TestApplyRedundancyRequest_ForceUpstreamStreaming(t *testing.T) {
	settings := DefaultRedundancySettings()
	applyRedundancyRequest(&settings, &adminRedundancyRequest{ForceUpstreamStreaming: boolPtr(false)})
	require.NotNil(t, settings.ForceUpstreamStreaming)
	require.False(t, *settings.ForceUpstreamStreaming)
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
	require.Equal(t, http.StatusOK, resp.StatusCode, string(raw))
	return string(raw)
}
