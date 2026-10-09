package main

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func decodeAssembled(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(body, &decoded))
	return decoded
}

func assembledContent(t *testing.T, body []byte) string {
	t.Helper()
	choices, ok := decodeAssembled(t, body)["choices"].([]any)
	require.True(t, ok, "choices missing from %s", body)
	require.NotEmpty(t, choices)
	message, ok := choices[0].(map[string]any)["message"].(map[string]any)
	require.True(t, ok, "message missing from %s", body)
	content, _ := message["content"].(string)
	return content
}

// The proxy's body is what a host actually receives, so it always asks for a stream and for the
// token counts every attempt is judged on.
func TestUpstreamPipeline_AsksTheHostToStream(t *testing.T) {
	body, req, err := upstreamChatRequestPipeline().Normalize([]byte(`{"messages":[{"role":"user","content":"hi"}]}`), false, defaultOutputTokenLimits(), "")
	require.NoError(t, err)

	sent := decodeAssembled(t, body)
	require.Equal(t, true, sent["stream"])
	require.Equal(t, map[string]any{"include_usage": true}, sent["stream_options"])
	require.False(t, req.Stream, "the client asked for one body, and that intent must survive the force")
	require.False(t, req.IncludeUsage)
}

// The gateway normalizes the same request for admission and caching, where a cached entry has to be
// replayed in the shape the client asked for.
func TestDefaultPipeline_LeavesTheClientShapeAlone(t *testing.T) {
	body, req, err := defaultChatRequestPipeline().Normalize([]byte(`{"messages":[{"role":"user","content":"hi"}]}`), false, defaultOutputTokenLimits(), "")
	require.NoError(t, err)

	sent := decodeAssembled(t, body)
	require.NotContains(t, sent, "stream")
	require.NotContains(t, sent, "stream_options")
	require.False(t, req.Stream)
	require.False(t, chatRequestStream(body))
}

func TestUpstreamPipeline_RemembersAClientThatAskedForUsage(t *testing.T) {
	_, req, err := upstreamChatRequestPipeline().Normalize([]byte(`{"messages":[{"role":"user","content":"hi"}],"stream":true,"stream_options":{"include_usage":true}}`), false, defaultOutputTokenLimits(), "")
	require.NoError(t, err)

	require.True(t, req.Stream)
	require.True(t, req.IncludeUsage)
}

func TestAssembleSSEBody_JoinsTheDeltasIntoOneMessage(t *testing.T) {
	stream := "data: {\"id\":\"cmpl-1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"He\"}}]}\n\n" +
		"data: {\"id\":\"cmpl-1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ll\"}}]}\n\n" +
		"data: {\"id\":\"cmpl-1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"o\"},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: [DONE]\n\n"

	assembled := aggregateSSEStream([]byte(stream), clientResponseIntent{})

	decoded := decodeAssembled(t, assembled)
	require.Equal(t, "chat.completion", decoded["object"])
	require.Equal(t, "Hello", assembledContent(t, assembled))
	choice := decoded["choices"].([]any)[0].(map[string]any)
	require.Equal(t, "stop", choice["finish_reason"])
	require.Equal(t, "assistant", choice["message"].(map[string]any)["role"])
	require.NotContains(t, choice, "delta")
}

// A host may answer a streaming request with the whole completion; that body is already the answer.
func TestAssembleSSEBody_KeepsACompletedBodyAsItStands(t *testing.T) {
	stream := "data: {\"id\":\"cmpl-1\",\"object\":\"chat.completion\",\"choices\":[{\"index\":0,\"message\":{\"role\":\"assistant\",\"content\":\"Hello\"}}]}\n\n"

	assembled := aggregateSSEStream([]byte(stream), clientResponseIntent{})

	require.Equal(t, "Hello", assembledContent(t, assembled))
	require.Equal(t, "chat.completion", decodeAssembled(t, assembled)["object"])
}

func TestAssembleSSEBody_OrdersChoicesByIndex(t *testing.T) {
	stream := "data: {\"choices\":[{\"index\":1,\"delta\":{\"content\":\"second\"}}]}\n\n" +
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"first\"}}]}\n\n"

	choices := decodeAssembled(t, aggregateSSEStream([]byte(stream), clientResponseIntent{}))["choices"].([]any)

	require.Len(t, choices, 2)
	require.Equal(t, "first", choices[0].(map[string]any)["message"].(map[string]any)["content"])
	require.Equal(t, "second", choices[1].(map[string]any)["message"].(map[string]any)["content"])
}

// Tool call arguments arrive in fragments and belong to the call at their own index.
func TestAssembleSSEBody_GrowsToolCallArgumentsPerIndex(t *testing.T) {
	stream := "data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call-1\",\"function\":{\"name\":\"lookup\",\"arguments\":\"{\\\"city\\\":\"}}]}}]}\n\n" +
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"\\\"Minsk\\\"\"}}]}}]}\n\n" +
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"}\"}}]}}]}\n\n"

	message := decodeAssembled(t, aggregateSSEStream([]byte(stream), clientResponseIntent{}))["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)
	call := message["tool_calls"].([]any)[0].(map[string]any)

	require.Equal(t, "call-1", call["id"], "an identity field is restated, not accumulated")
	require.Equal(t, "lookup", call["function"].(map[string]any)["name"])
	require.Equal(t, `{"city":"Minsk"}`, call["function"].(map[string]any)["arguments"])
	require.Nil(t, message["content"], "a tool call answers with a null content, never with the field absent")
}

// Some backends restate the whole arguments each chunk instead of sending fragments; appending
// those would hand the client a call it cannot parse.
func TestAssembleSSEBody_ReplacesArgumentsAHostResendsWhole(t *testing.T) {
	stream := "data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"{\\\"city\\\":\"}}]}}]}\n\n" +
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"{\\\"city\\\":\\\"Minsk\\\"\"}}]}}]}\n\n" +
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"{\\\"city\\\":\\\"Minsk\\\"}\"}}]}}]}\n\n"

	call := decodeAssembled(t, aggregateSSEStream([]byte(stream), clientResponseIntent{}))["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)["tool_calls"].([]any)[0].(map[string]any)

	require.Equal(t, `{"city":"Minsk"}`, call["function"].(map[string]any)["arguments"])
}

// A chunk carrying a bareword no JSON parser accepts must still contribute its content.
func TestAssembleSSEBody_KeepsContentBesideANonFiniteNumber(t *testing.T) {
	stream := "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hi\"},\"logprobs\":{\"content\":[{\"token\":\"Hi\",\"logprob\":-Infinity}]}}]}\n\n"

	assembled := aggregateSSEStream([]byte(stream), clientResponseIntent{})

	require.Equal(t, "Hi", assembledContent(t, assembled))
}

func TestReplaceNonFiniteNumbers_RewritesBareLiteralsLeavesStrings(t *testing.T) {
	rewritten, ok := replaceNonFiniteNumbers([]byte(`{"a":-Infinity,"b":Infinity,"c":NaN,"d":"-Infinity"}`))
	require.True(t, ok)
	require.JSONEq(t, `{"a":null,"b":null,"c":null,"d":"-Infinity"}`, string(rewritten))

	_, ok = replaceNonFiniteNumbers([]byte(`{"a":1}`))
	require.False(t, ok)
}

func TestAssembleSSEBody_LeavesGeneratedMarkupUnescaped(t *testing.T) {
	stream := "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"<b>\"}}]}\n\n" +
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\" & </b>\"}}]}\n\n"

	assembled := aggregateSSEStream([]byte(stream), clientResponseIntent{})

	require.Equal(t, "<b> & </b>", assembledContent(t, assembled))
}

// SSE lets a host split one object across data lines, and a client joins them before parsing.
func TestAssembleSSEBody_JoinsAnEventSplitAcrossDataLines(t *testing.T) {
	stream := "data: {\"choices\":[{\"index\":0,\n" +
		"data: \"delta\":{\"content\":\"Hi\"}}]}\n\n"

	require.Equal(t, "Hi", assembledContent(t, aggregateSSEStream([]byte(stream), clientResponseIntent{})))
}

func TestAssembleSSEBody_ReportsAStreamThatCarriedNothing(t *testing.T) {
	require.JSONEq(t, string(noResponseDataBody), string(aggregateSSEStream([]byte("data: [DONE]\n\n"), clientResponseIntent{})))
	require.JSONEq(t, string(noResponseDataBody), string(aggregateSSEStream(nil, clientResponseIntent{})))
}

// A host that answers with a plain JSON error never frames it as SSE; that body is the answer.
func TestAssembleSSEBody_PassesAnUnframedBodyThrough(t *testing.T) {
	body := []byte(`{"error":{"message":"model not found"}}`)

	require.JSONEq(t, string(body), string(aggregateSSEStream(body, clientResponseIntent{})))
}

func TestRewriteStreamingPayload_DropsTheUsageChunkTheClientNeverAskedFor(t *testing.T) {
	payload := []byte("data: {\"id\":\"cmpl-1\",\"object\":\"chat.completion.chunk\",\"choices\":[],\"usage\":{\"prompt_tokens\":7,\"completion_tokens\":1}}\n\n")

	require.Empty(t, rewriteStreamingPayload(payload, clientResponseIntent{}))
	require.Equal(t, payload, rewriteStreamingPayload(payload, clientResponseIntent{keepUsage: true}))
}

// A host that reports usage alongside content must keep the content: only the usage goes.
func TestRewriteStreamingPayload_KeepsContentWhenOnlyTheUsageIsUnwanted(t *testing.T) {
	payload := []byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hi\"}}],\"usage\":{\"completion_tokens\":1}}\n\n")

	rewritten := rewriteStreamingPayload(payload, clientResponseIntent{})

	require.Contains(t, string(rewritten), `"content":"Hi"`)
	require.NotContains(t, string(rewritten), "usage")
}
