//go:build testenvci

package citest

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"devshard/testenv/citest/harness"
	"devshard/testenv/config"
	"devshard/testenv/mockopenai"

	"github.com/stretchr/testify/require"
)

func bootForcedUpstreamStack(t *testing.T, prefix string) (*harness.Stack, *config.File, harness.Endpoints) {
	t.Helper()
	stack, cfg, eps := harness.BootAdversarialStack(t, prefix)
	t.Cleanup(func() {
		if t.Failed() {
			harness.DumpComposeLogs(t, stack, "devshardctl", "versiond-0", "versiond-1", "mock-openai")
		}
	})
	return stack, cfg, eps
}

// TestGatewayForcedStreamClientShape checks that ForceUpstreamStreaming never
// changes the client-visible Content-Type / body shape: stream:false stays JSON,
// stream:true stays SSE ending in [DONE], with the flag both on and off.
func TestGatewayForcedStreamClientShape(t *testing.T) {
	harness.SkipUnlessEnv(t, "TESTENV_CITEST")
	harness.RequireDocker(t)

	_, cfg, eps := bootForcedUpstreamStack(t, "citest-forced-stream-shape-*")
	client := harness.GatewayChatClient()
	model := config.PrimaryModelID(cfg)
	admin := harness.TestenvAdminAPIKey

	assertShapes := func(t *testing.T, forceOn bool) {
		t.Helper()
		harness.PatchGatewayForceUpstreamStreaming(t, client, eps.GatewayHTTP, forceOn)

		nonStream := harness.ChatCompletionRequest{
			Model: model,
			Messages: []harness.ChatMessage{
				{Role: "user", Content: "citest forced-stream shape non-stream " + t.Name()},
			},
			MaxTokens: 32,
			Stream:    false,
		}
		harness.Step(t, "non-stream client (force=%v)", forceOn)
		got := harness.PostGatewayChatHTTP(t, client, eps.GatewayHTTP, admin, nonStream)
		require.Equal(t, 200, got.Status, "body=%s", got.Body)
		require.Contains(t, strings.ToLower(got.ContentType), "application/json")
		require.NotContains(t, string(got.Body), "data: [DONE]")
		require.NotContains(t, string(got.Body), "text/event-stream")
		var resp harness.ChatCompletionResponse
		require.NoError(t, json.Unmarshal(got.Body, &resp))
		require.NotEmpty(t, resp.Choices)
		harness.RequireMockOpenAIContent(t, resp.Choices[0].Message.Content)

		stream := harness.ChatCompletionRequest{
			Model: model,
			Messages: []harness.ChatMessage{
				{Role: "user", Content: "citest forced-stream shape stream " + t.Name()},
			},
			MaxTokens: 32,
			Stream:    true,
		}
		harness.Step(t, "streaming client (force=%v)", forceOn)
		gotStream := harness.PostGatewayChatHTTP(t, client, eps.GatewayHTTP, admin, stream)
		require.Equal(t, 200, gotStream.Status, "body=%s", gotStream.Body)
		require.Contains(t, strings.ToLower(gotStream.ContentType), "text/event-stream")
		chunks, sawDone := harness.ParseSSEDataChunks(gotStream.Body)
		require.True(t, sawDone, "stream missing data: [DONE]")
		content := harness.AssembleSSEContent(chunks)
		harness.RequireMockOpenAIContent(t, content)
	}

	t.Run("force_on", func(t *testing.T) { assertShapes(t, true) })
	t.Run("force_off", func(t *testing.T) { assertShapes(t, false) })
}

// TestGatewayForcedStreamUsageSuppression checks client include_usage intent under
// ForceUpstreamStreaming: streaming clients without include_usage never see usage;
// with it they get one final usage chunk; non-stream always gets usage in the aggregate.
func TestGatewayForcedStreamUsageSuppression(t *testing.T) {
	harness.SkipUnlessEnv(t, "TESTENV_CITEST")
	harness.RequireDocker(t)

	_, cfg, eps := bootForcedUpstreamStack(t, "citest-forced-stream-usage-*")
	client := harness.GatewayChatClient()
	model := config.PrimaryModelID(cfg)
	admin := harness.TestenvAdminAPIKey
	harness.PatchGatewayForceUpstreamStreaming(t, client, eps.GatewayHTTP, true)

	prompt := "citest forced-stream usage " + t.Name()

	harness.Step(t, "streaming client without include_usage")
	noUsage := harness.ChatCompletionRequest{
		Model: model,
		Messages: []harness.ChatMessage{
			{Role: "user", Content: prompt + " no-usage"},
		},
		MaxTokens: 32,
		Stream:    true,
	}
	got := harness.PostGatewayChatHTTP(t, client, eps.GatewayHTTP, admin, noUsage)
	require.Equal(t, 200, got.Status, "body=%s", got.Body)
	chunks, sawDone := harness.ParseSSEDataChunks(got.Body)
	require.True(t, sawDone)
	require.False(t, harness.SSEChunksHaveTopLevelUsage(chunks),
		"usage leaked to streaming client without include_usage: %s", got.Body)
	require.Zero(t, harness.CountSSEUsageOnlyChunks(chunks))

	harness.Step(t, "streaming client with include_usage")
	withUsage := harness.ChatCompletionRequest{
		Model: model,
		Messages: []harness.ChatMessage{
			{Role: "user", Content: prompt + " with-usage"},
		},
		MaxTokens:     32,
		Stream:        true,
		StreamOptions: &harness.ChatStreamOptions{IncludeUsage: true},
	}
	gotUsage := harness.PostGatewayChatHTTP(t, client, eps.GatewayHTTP, admin, withUsage)
	require.Equal(t, 200, gotUsage.Status, "body=%s", gotUsage.Body)
	usageChunks, sawDone := harness.ParseSSEDataChunks(gotUsage.Body)
	require.True(t, sawDone)
	require.True(t, harness.SSEChunksHaveTopLevelUsage(usageChunks),
		"expected usage chunk when include_usage=true: %s", gotUsage.Body)
	require.Equal(t, 1, harness.CountSSEUsageOnlyChunks(usageChunks)+countMixedUsageChunks(usageChunks),
		"expected exactly one usage-bearing terminal chunk")
	requireUsageTokens(t, usageChunks)

	harness.Step(t, "non-streaming client always gets usage in aggregate")
	agg := harness.PostGatewayChatCompletion(t, client, eps.GatewayHTTP, admin, harness.ChatCompletionRequest{
		Model: model,
		Messages: []harness.ChatMessage{
			{Role: "user", Content: prompt + " aggregate"},
		},
		MaxTokens: 32,
	})
	require.NotEmpty(t, agg.Usage)
	require.Greater(t, usageNumber(agg.Usage["prompt_tokens"]), 0.0)
	require.Greater(t, usageNumber(agg.Usage["completion_tokens"]), 0.0)
}

func countMixedUsageChunks(chunks []map[string]any) int {
	n := 0
	for _, chunk := range chunks {
		u, ok := chunk["usage"].(map[string]any)
		if !ok || len(u) == 0 {
			continue
		}
		choices, _ := chunk["choices"].([]any)
		if len(choices) > 0 {
			n++
		}
	}
	return n
}

func requireUsageTokens(t *testing.T, chunks []map[string]any) {
	t.Helper()
	for _, chunk := range chunks {
		u, ok := chunk["usage"].(map[string]any)
		if !ok || len(u) == 0 {
			continue
		}
		require.Greater(t, usageNumber(u["prompt_tokens"]), 0.0)
		require.Greater(t, usageNumber(u["completion_tokens"]), 0.0)
		return
	}
	t.Fatal("no usage chunk with token counts")
}

func usageNumber(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case json.Number:
		f, _ := n.Float64()
		return f
	default:
		return 0
	}
}

// TestGatewayForcedStreamLogprobStrip checks that clients who did not ask for
// logprobs never see them (gateway forces logprobs upstream regardless of the
// force-upstream flag), and clients who did get logprobs.content capped to their
// top_logprobs ask.
func TestGatewayForcedStreamLogprobStrip(t *testing.T) {
	harness.SkipUnlessEnv(t, "TESTENV_CITEST")
	harness.RequireDocker(t)

	_, cfg, eps := bootForcedUpstreamStack(t, "citest-forced-stream-logprobs-*")
	client := harness.GatewayChatClient()
	model := config.PrimaryModelID(cfg)
	admin := harness.TestenvAdminAPIKey

	for _, forceOn := range []bool{true, false} {
		forceOn := forceOn
		name := "force_off"
		if forceOn {
			name = "force_on"
		}
		t.Run(name, func(t *testing.T) {
			harness.PatchGatewayForceUpstreamStreaming(t, client, eps.GatewayHTTP, forceOn)
			prompt := "citest forced-stream logprobs " + t.Name()

			harness.Step(t, "client without logprobs (stream=false)")
			noLP := harness.PostGatewayChatHTTP(t, client, eps.GatewayHTTP, admin, harness.ChatCompletionRequest{
				Model: model,
				Messages: []harness.ChatMessage{
					{Role: "user", Content: prompt + " no-lp-json"},
				},
				MaxTokens: 24,
			})
			require.Equal(t, 200, noLP.Status, "body=%s", noLP.Body)
			require.False(t, harness.BodyMentionsForbiddenLogprobKeys(noLP.Body),
				"logprob fields leaked into aggregate: %s", noLP.Body)

			harness.Step(t, "client without logprobs (stream=true)")
			noLPStream := harness.PostGatewayChatHTTP(t, client, eps.GatewayHTTP, admin, harness.ChatCompletionRequest{
				Model: model,
				Messages: []harness.ChatMessage{
					{Role: "user", Content: prompt + " no-lp-sse"},
				},
				MaxTokens: 24,
				Stream:    true,
			})
			require.Equal(t, 200, noLPStream.Status, "body=%s", noLPStream.Body)
			require.False(t, harness.BodyMentionsForbiddenLogprobKeys(noLPStream.Body),
				"logprob fields leaked into stream: %s", noLPStream.Body)

			harness.Step(t, "client with logprobs but without top_logprobs")
			lpNoTop := harness.PostGatewayChatHTTP(t, client, eps.GatewayHTTP, admin, harness.ChatCompletionRequest{
				Model: model,
				Messages: []harness.ChatMessage{
					{Role: "user", Content: prompt + " lp-no-top"},
				},
				MaxTokens: 24,
				Logprobs:  true,
			})
			require.Equal(t, 200, lpNoTop.Status, "body=%s", lpNoTop.Body)
			var noTopPayload map[string]any
			require.NoError(t, json.Unmarshal(lpNoTop.Body, &noTopPayload))
			require.Greater(t, harness.LogprobContentEntryCount(noTopPayload), 0,
				"expected logprobs.content when client asked: %s", lpNoTop.Body)
			require.Equal(t, 0, harness.MaxTopLogprobsWidth(noTopPayload),
				"top_logprobs must be emptied when client did not ask: %s", lpNoTop.Body)

			harness.Step(t, "client with logprobs + top_logprobs")
			withLP := harness.PostGatewayChatHTTP(t, client, eps.GatewayHTTP, admin, harness.ChatCompletionRequest{
				Model: model,
				Messages: []harness.ChatMessage{
					{Role: "user", Content: prompt + " with-lp"},
				},
				MaxTokens:   24,
				Logprobs:    true,
				TopLogprobs: 2,
			})
			require.Equal(t, 200, withLP.Status, "body=%s", withLP.Body)
			var payload map[string]any
			require.NoError(t, json.Unmarshal(withLP.Body, &payload))
			require.Greater(t, harness.LogprobContentEntryCount(payload), 0,
				"expected logprobs.content when client asked: %s", withLP.Body)
			// Contract: any client top_logprobs > 0 keeps ForcedTopLogprobs (5);
			// we do not truncate to the client's numeric ask.
			require.Equal(t, 5, harness.MaxTopLogprobsWidth(payload),
				"expected forced top_logprobs width when client asked: %s", withLP.Body)
		})
	}
}

// TestGatewayForcedStreamAggregateMatchesUnforced compares the same non-stream
// prompt with the force flag on vs off against the real stack.
func TestGatewayForcedStreamAggregateMatchesUnforced(t *testing.T) {
	harness.SkipUnlessEnv(t, "TESTENV_CITEST")
	harness.RequireDocker(t)

	_, cfg, eps := bootForcedUpstreamStack(t, "citest-forced-stream-diff-*")
	client := harness.GatewayChatClient()
	model := config.PrimaryModelID(cfg)
	admin := harness.TestenvAdminAPIKey
	seed := 7
	req := harness.ChatCompletionRequest{
		Model: model,
		Messages: []harness.ChatMessage{
			{Role: "user", Content: "citest forced-stream differential aggregate match"},
		},
		MaxTokens: 40,
		Seed:      &seed,
	}

	harness.PatchGatewayForceUpstreamStreaming(t, client, eps.GatewayHTTP, true)
	harness.Step(t, "aggregate with force_upstream_streaming on")
	forced := harness.PostGatewayChatHTTP(t, client, eps.GatewayHTTP, admin, req)
	require.Equal(t, 200, forced.Status, "body=%s", forced.Body)

	harness.PatchGatewayForceUpstreamStreaming(t, client, eps.GatewayHTTP, false)
	harness.Step(t, "aggregate with force_upstream_streaming off")
	unforced := harness.PostGatewayChatHTTP(t, client, eps.GatewayHTTP, admin, req)
	require.Equal(t, 200, unforced.Status, "body=%s", unforced.Body)

	var a, b harness.ChatCompletionResponse
	require.NoError(t, json.Unmarshal(forced.Body, &a))
	require.NoError(t, json.Unmarshal(unforced.Body, &b))
	require.Equal(t, a.Choices[0].Message.Content, b.Choices[0].Message.Content)
	require.Equal(t, a.Choices[0].FinishReason, b.Choices[0].FinishReason)
	require.Equal(t, a.Model, b.Model)
	require.Equal(t, a.Object, b.Object)
	// completion_tokens track the same assistant text. prompt_tokens may differ
	// under mock-openai (it estimates from the wire body, which grows when the
	// gateway force-enables stream/logprobs/stream_options upstream).
	require.Equal(t, usageNumber(a.Usage["completion_tokens"]), usageNumber(b.Usage["completion_tokens"]))
	require.Greater(t, usageNumber(a.Usage["prompt_tokens"]), 0.0)
	require.Greater(t, usageNumber(b.Usage["prompt_tokens"]), 0.0)
}

// TestGatewayForcedStreamCacheIsolation checks stream vs non-stream clients never
// share a cached body under ForceUpstreamStreaming, and that cache hits keep shape.
func TestGatewayForcedStreamCacheIsolation(t *testing.T) {
	harness.SkipUnlessEnv(t, "TESTENV_CITEST")
	harness.RequireDocker(t)

	_, cfg, eps := bootForcedUpstreamStack(t, "citest-forced-stream-cache-*")
	client := harness.GatewayChatClient()
	model := config.PrimaryModelID(cfg)
	admin := harness.TestenvAdminAPIKey
	harness.PatchGatewayForceUpstreamStreaming(t, client, eps.GatewayHTTP, true)

	messages := []harness.ChatMessage{
		{Role: "user", Content: "citest forced-stream cache isolation unique body"},
	}
	nonStream := harness.ChatCompletionRequest{Model: model, Messages: messages, MaxTokens: 32}
	stream := harness.ChatCompletionRequest{Model: model, Messages: messages, MaxTokens: 32, Stream: true}

	harness.Step(t, "first non-stream miss")
	json1 := harness.PostGatewayChatHTTP(t, client, eps.GatewayHTTP, admin, nonStream)
	require.Equal(t, 200, json1.Status)
	require.Contains(t, strings.ToLower(json1.ContentType), "application/json")

	harness.Step(t, "first stream miss")
	sse1 := harness.PostGatewayChatHTTP(t, client, eps.GatewayHTTP, admin, stream)
	require.Equal(t, 200, sse1.Status)
	require.Contains(t, strings.ToLower(sse1.ContentType), "text/event-stream")
	_, sawDone := harness.ParseSSEDataChunks(sse1.Body)
	require.True(t, sawDone)

	harness.Step(t, "second non-stream hit keeps JSON shape")
	json2 := harness.PostGatewayChatHTTP(t, client, eps.GatewayHTTP, admin, nonStream)
	require.Equal(t, 200, json2.Status)
	require.Contains(t, strings.ToLower(json2.ContentType), "application/json")
	require.NotContains(t, string(json2.Body), "data: [DONE]")
	var resp harness.ChatCompletionResponse
	require.NoError(t, json.Unmarshal(json2.Body, &resp))
	require.NotEmpty(t, resp.Choices[0].Message.Content)

	harness.Step(t, "second stream hit keeps SSE shape")
	sse2 := harness.PostGatewayChatHTTP(t, client, eps.GatewayHTTP, admin, stream)
	require.Equal(t, 200, sse2.Status)
	require.Contains(t, strings.ToLower(sse2.ContentType), "text/event-stream")
	_, sawDone = harness.ParseSSEDataChunks(sse2.Body)
	require.True(t, sawDone)
}

// TestGatewayForcedStreamFlagFlip flips force_upstream_streaming via admin while
// a request is in flight and between requests; each request keeps the client shape
// it started with (per-request ForceUpstreamStreaming snapshot).
func TestGatewayForcedStreamFlagFlip(t *testing.T) {
	harness.SkipUnlessEnv(t, "TESTENV_CITEST")
	harness.RequireDocker(t)

	_, cfg, eps := bootForcedUpstreamStack(t, "citest-forced-stream-flip-*")
	client := harness.GatewayChatClient()
	model := config.PrimaryModelID(cfg)
	admin := harness.TestenvAdminAPIKey
	mockURL := eps.MockOpenAIHTTP
	t.Cleanup(func() {
		harness.ResetMockOpenAIFault(t, client, mockURL)
	})

	harness.PatchGatewayForceUpstreamStreaming(t, client, eps.GatewayHTTP, true)
	delayMS := 60
	harness.PatchMockOpenAIFault(t, client, mockURL, mockopenai.FaultPatch{StreamChunkDelay: &delayMS})

	type httpOutcome struct {
		res harness.GatewayChatHTTPResult
		err error
	}
	started := make(chan struct{})
	done := make(chan httpOutcome, 1)
	go func() {
		req := harness.ChatCompletionRequest{
			Model: model,
			Messages: []harness.ChatMessage{
				{Role: "user", Content: "citest forced-stream mid-flight flip non-stream"},
			},
			MaxTokens: 48,
		}
		close(started)
		// Avoid testify FailNow from a worker goroutine: marshal + Do manually.
		data, err := json.Marshal(req)
		if err != nil {
			done <- httpOutcome{err: err}
			return
		}
		httpReq, err := http.NewRequest(http.MethodPost, eps.GatewayHTTP+"/v1/chat/completions", bytes.NewReader(data))
		if err != nil {
			done <- httpOutcome{err: err}
			return
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Authorization", "Bearer "+admin)
		resp, err := client.Do(httpReq)
		if err != nil {
			done <- httpOutcome{err: err}
			return
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		done <- httpOutcome{res: harness.GatewayChatHTTPResult{
			Status:      resp.StatusCode,
			ContentType: resp.Header.Get("Content-Type"),
			Body:        body,
		}}
	}()
	<-started
	time.Sleep(150 * time.Millisecond)
	harness.Step(t, "flip force_upstream_streaming off while request in flight")
	harness.PatchGatewayForceUpstreamStreaming(t, client, eps.GatewayHTTP, false)
	outcome := <-done
	require.NoError(t, outcome.err)
	got := outcome.res
	require.Equal(t, 200, got.Status, "body=%s", got.Body)
	require.Contains(t, strings.ToLower(got.ContentType), "application/json",
		"in-flight non-stream must keep JSON after mid-flight flag flip")
	require.NotContains(t, string(got.Body), "data: [DONE]")

	harness.ResetMockOpenAIFault(t, client, mockURL)
	harness.Step(t, "new non-stream request after flip still JSON")
	after := harness.PostGatewayChatHTTP(t, client, eps.GatewayHTTP, admin, harness.ChatCompletionRequest{
		Model: model,
		Messages: []harness.ChatMessage{
			{Role: "user", Content: "citest forced-stream after flip"},
		},
		MaxTokens: 32,
	})
	require.Equal(t, 200, after.Status)
	require.Contains(t, strings.ToLower(after.ContentType), "application/json")

	harness.Step(t, "re-enable force and stream client still SSE")
	harness.PatchGatewayForceUpstreamStreaming(t, client, eps.GatewayHTTP, true)
	stream := harness.PostGatewayChatHTTP(t, client, eps.GatewayHTTP, admin, harness.ChatCompletionRequest{
		Model: model,
		Messages: []harness.ChatMessage{
			{Role: "user", Content: "citest forced-stream after re-enable stream"},
		},
		MaxTokens: 32,
		Stream:    true,
	})
	require.Equal(t, 200, stream.Status)
	require.Contains(t, strings.ToLower(stream.ContentType), "text/event-stream")
	_, sawDone := harness.ParseSSEDataChunks(stream.Body)
	require.True(t, sawDone)
}
