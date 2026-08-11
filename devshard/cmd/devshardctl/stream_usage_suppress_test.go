package main

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRewriteStreamingPayload_SuppressesUsageChunkWhenNotRequested(t *testing.T) {
	payload := []byte(
		`data: {"id":"c","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"Hi"},"finish_reason":null}]}` + "\n\n" +
			`data: {"id":"c","object":"chat.completion.chunk","choices":[],"usage":{"prompt_tokens":3,"completion_tokens":1}}` + "\n\n" +
			`data: [DONE]` + "\n\n",
	)

	rewritten := rewriteStreamingPayload(payload, logprobClientIntent{}, streamClientIntent{wantsStream: true})
	events := parseSSEChunks(t, string(rewritten))
	require.Len(t, events, 1)
	require.Equal(t, "Hi", events[0]["choices"].([]any)[0].(map[string]any)["delta"].(map[string]any)["content"])
	require.Contains(t, string(rewritten), "data: [DONE]")
	require.NotContains(t, string(rewritten), `"usage"`)
}

func TestRewriteStreamingPayload_KeepsUsageChunkWhenRequested(t *testing.T) {
	usageEvt := `{"id":"c","object":"chat.completion.chunk","choices":[],"usage":{"prompt_tokens":3,"completion_tokens":1}}`
	payload := []byte(
		`data: {"id":"c","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"Hi"},"finish_reason":null}]}` + "\n\n" +
			`data: ` + usageEvt + "\n\n" +
			`data: [DONE]` + "\n\n",
	)

	rewritten := rewriteStreamingPayload(payload, logprobClientIntent{}, streamClientIntent{wantsStream: true, wantsUsage: true})
	events := parseSSEChunks(t, string(rewritten))
	require.Len(t, events, 2)
	require.JSONEq(t, usageEvt, mustMarshalJSON(t, events[1]))
}

func TestRewriteStreamingPayload_DoesNotDropContentChunkWithUsageNull(t *testing.T) {
	payload := []byte(
		`data: {"id":"c","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"x"},"finish_reason":null}],"usage":null}` + "\n\n",
	)
	rewritten := rewriteStreamingPayload(payload, logprobClientIntent{}, streamClientIntent{wantsStream: true})
	events := parseSSEChunks(t, string(rewritten))
	require.Len(t, events, 1)
	require.Equal(t, "x", events[0]["choices"].([]any)[0].(map[string]any)["delta"].(map[string]any)["content"])
}

func TestRewriteStreamingPayload_StripsUsageFromMixedChunk(t *testing.T) {
	// A host that puts usage on the same chunk as the finish_reason choice must
	// lose the usage pair, not the chunk.
	payload := []byte(
		`data: {"id":"c","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1}}` + "\n\n" +
			`data: [DONE]` + "\n\n",
	)

	rewritten := rewriteStreamingPayload(payload, logprobClientIntent{}, streamClientIntent{wantsStream: true})
	require.NotContains(t, string(rewritten), `"usage"`)
	require.Contains(t, string(rewritten), "data: [DONE]")

	events := parseSSEChunks(t, string(rewritten))
	require.Len(t, events, 1)
	choice := events[0]["choices"].([]any)[0].(map[string]any)
	require.Equal(t, "stop", choice["finish_reason"])
}

func TestRewriteStreamingPayload_KeepsMixedUsageChunkWhenRequested(t *testing.T) {
	evt := `{"id":"c","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1}}`
	payload := []byte(`data: ` + evt + "\n\n")

	rewritten := rewriteStreamingPayload(payload, logprobClientIntent{}, streamClientIntent{wantsStream: true, wantsUsage: true})
	events := parseSSEChunks(t, string(rewritten))
	require.Len(t, events, 1)
	require.JSONEq(t, evt, mustMarshalJSON(t, events[0]))
}

func TestRewriteStreamingPayload_StripsUsageFromContentChunk(t *testing.T) {
	// Same leak, but the usage rides a chunk that still carries content.
	payload := []byte(
		`data: {"id":"c","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"Hi"},"finish_reason":null}],"usage":{"prompt_tokens":3,"completion_tokens":1}}` + "\n\n",
	)

	rewritten := rewriteStreamingPayload(payload, logprobClientIntent{}, streamClientIntent{wantsStream: true})
	require.NotContains(t, string(rewritten), `"usage"`)

	events := parseSSEChunks(t, string(rewritten))
	require.Len(t, events, 1)
	require.Equal(t, "Hi", events[0]["choices"].([]any)[0].(map[string]any)["delta"].(map[string]any)["content"])
}

func TestRewriteStreamingPayload_LeavesNestedUsageAlone(t *testing.T) {
	// "usage" only inside a delta is client content, not the forced trailer.
	evt := `{"id":"c","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"{\"usage\":1}"},"finish_reason":null}]}`
	payload := []byte(`data: ` + evt + "\n\n")

	rewritten := rewriteStreamingPayload(payload, logprobClientIntent{}, streamClientIntent{wantsStream: true})
	events := parseSSEChunks(t, string(rewritten))
	require.Len(t, events, 1)
	require.JSONEq(t, evt, mustMarshalJSON(t, events[0]))
}

func TestRewriteStreamingPayload_SynthesizedUsageHonorsIntent(t *testing.T) {
	payload := []byte(`data: {"id":"cmpl-1","object":"chat.completion","created":123,"model":"Qwen","choices":[{"index":0,"message":{"role":"assistant","content":"Hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":1}}` + "\n\n")

	without := rewriteStreamingPayload(payload, logprobClientIntent{}, streamClientIntent{wantsStream: true})
	events := parseSSEChunks(t, string(without))
	for _, evt := range events {
		_, hasUsage := evt["usage"]
		require.False(t, hasUsage, "usage must not be synthesized when client did not ask")
	}

	with := rewriteStreamingPayload(payload, logprobClientIntent{}, streamClientIntent{wantsStream: true, wantsUsage: true})
	events = parseSSEChunks(t, string(with))
	require.GreaterOrEqual(t, len(events), 2)
	last := events[len(events)-1]
	require.Contains(t, last, "usage")
	require.Empty(t, last["choices"])
}

func TestAggregateSSEStream_StillCarriesUsageRegardlessOfClientIntent(t *testing.T) {
	// Aggregated JSON path is independent of streaming usage suppression.
	raw := sseData(
		`{"id":"cmpl-1","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"content":"Hi"},"finish_reason":"stop"}]}`,
		`{"id":"cmpl-1","object":"chat.completion.chunk","created":1,"model":"m","choices":[],"usage":{"prompt_tokens":3,"completion_tokens":1}}`,
	)
	got := aggregateSSEStream(raw, logprobClientIntent{})
	var resp map[string]any
	require.NoError(t, json.Unmarshal(got, &resp))
	usage := resp["usage"].(map[string]any)
	require.Equal(t, float64(3), usage["prompt_tokens"])
	require.Equal(t, float64(1), usage["completion_tokens"])
}

func mustMarshalJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return string(b)
}
