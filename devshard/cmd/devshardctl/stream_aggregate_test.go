package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"devshard/transport"

	"github.com/stretchr/testify/require"
)

func sseData(payloads ...string) []byte {
	var b strings.Builder
	for _, p := range payloads {
		b.WriteString("data: ")
		b.WriteString(p)
		b.WriteString("\n\n")
	}
	b.WriteString("data: [DONE]\n\n")
	return []byte(b.String())
}

func TestAggregateSSEStream_MultiChunkContent(t *testing.T) {
	raw := sseData(
		`{"id":"cmpl-1","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`,
		`{"id":"cmpl-1","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"content":"Hel"},"finish_reason":null}]}`,
		`{"id":"cmpl-1","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"content":"lo"},"finish_reason":"stop"}]}`,
		`{"id":"cmpl-1","object":"chat.completion.chunk","created":1,"model":"m","choices":[],"usage":{"prompt_tokens":3,"completion_tokens":2}}`,
	)
	got := aggregateSSEStream(raw, logprobClientIntent{})
	var resp map[string]any
	require.NoError(t, json.Unmarshal(got, &resp))
	require.Equal(t, "chat.completion", resp["object"])
	require.Equal(t, "cmpl-1", resp["id"])
	require.Equal(t, "m", resp["model"])
	choices := resp["choices"].([]any)
	require.Len(t, choices, 1)
	ch := choices[0].(map[string]any)
	msg := ch["message"].(map[string]any)
	require.Equal(t, "assistant", msg["role"])
	require.Equal(t, "Hello", msg["content"])
	require.Equal(t, "stop", ch["finish_reason"])
	usage := resp["usage"].(map[string]any)
	require.Equal(t, float64(3), usage["prompt_tokens"])
	require.Equal(t, float64(2), usage["completion_tokens"])
}

func TestAggregateSSEStream_ReasoningAndRefusal(t *testing.T) {
	raw := sseData(
		`{"id":"c","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"think"},"finish_reason":null}]}`,
		`{"id":"c","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"reasoning_content":" more","refusal":"no"},"finish_reason":"content_filter"}]}`,
	)
	got := aggregateSSEStream(raw, logprobClientIntent{})
	var resp map[string]any
	require.NoError(t, json.Unmarshal(got, &resp))
	msg := resp["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)
	require.Equal(t, "think more", msg["reasoning_content"])
	require.Equal(t, "no", msg["refusal"])
	require.Equal(t, "content_filter", resp["choices"].([]any)[0].(map[string]any)["finish_reason"])
}

func TestAggregateSSEStream_ToolCallFragments(t *testing.T) {
	raw := sseData(
		`{"id":"c","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"add","arguments":"{\"a\":"}}]},"finish_reason":null}]}`,
		`{"id":"c","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"1}"}}]},"finish_reason":"tool_calls"}]}`,
	)
	got := aggregateSSEStream(raw, logprobClientIntent{})
	var resp map[string]any
	require.NoError(t, json.Unmarshal(got, &resp))
	msg := resp["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)
	require.Nil(t, msg["content"])
	tcs := msg["tool_calls"].([]any)
	require.Len(t, tcs, 1)
	tc := tcs[0].(map[string]any)
	require.Equal(t, "call_1", tc["id"])
	require.Equal(t, "function", tc["type"])
	fn := tc["function"].(map[string]any)
	require.Equal(t, "add", fn["name"])
	require.Equal(t, `{"a":1}`, fn["arguments"])
	require.Equal(t, "tool_calls", resp["choices"].([]any)[0].(map[string]any)["finish_reason"])
}

func TestAggregateSSEStream_InterleavedN2(t *testing.T) {
	raw := sseData(
		`{"id":"c","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":"A"},"finish_reason":null}]}`,
		`{"id":"c","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":1,"delta":{"role":"assistant","content":"B"},"finish_reason":null}]}`,
		`{"id":"c","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"content":"1"},"finish_reason":"stop"}]}`,
		`{"id":"c","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":1,"delta":{"content":"2"},"finish_reason":"stop"}]}`,
	)
	got := aggregateSSEStream(raw, logprobClientIntent{})
	var resp map[string]any
	require.NoError(t, json.Unmarshal(got, &resp))
	choices := resp["choices"].([]any)
	require.Len(t, choices, 2)
	require.Equal(t, float64(0), choices[0].(map[string]any)["index"])
	require.Equal(t, "A1", choices[0].(map[string]any)["message"].(map[string]any)["content"])
	require.Equal(t, float64(1), choices[1].(map[string]any)["index"])
	require.Equal(t, "B2", choices[1].(map[string]any)["message"].(map[string]any)["content"])
}

func TestAggregateSSEStream_LogprobsConcat(t *testing.T) {
	raw := sseData(
		`{"id":"c","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"content":"H"},"logprobs":{"content":[{"token":"H","logprob":-0.1,"top_logprobs":[{"token":"H","logprob":-0.1}]}]},"finish_reason":null}]}`,
		`{"id":"c","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"content":"i"},"logprobs":{"content":[{"token":"i","logprob":-0.2,"top_logprobs":[{"token":"i","logprob":-0.2}]}]},"finish_reason":"stop"}]}`,
	)
	got := aggregateSSEStream(raw, logprobClientIntent{keepLogprobs: true, keepTopLogprobs: true})
	var resp map[string]any
	require.NoError(t, json.Unmarshal(got, &resp))
	lp := resp["choices"].([]any)[0].(map[string]any)["logprobs"].(map[string]any)["content"].([]any)
	require.Len(t, lp, 2)
	require.Equal(t, "H", lp[0].(map[string]any)["token"])
	require.Equal(t, "i", lp[1].(map[string]any)["token"])
	require.Len(t, lp[0].(map[string]any)["top_logprobs"].([]any), 1)
}

func TestAggregateSSEStream_DropsLogprobsWhenClientDidNotAsk(t *testing.T) {
	raw := sseData(
		`{"id":"c","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"content":"H"},"logprobs":{"content":[{"token":"H","logprob":-0.1,"top_logprobs":[{"token":"H","logprob":-0.1}]}]},"finish_reason":null}]}`,
		`{"id":"c","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"content":"i"},"logprobs":{"content":[{"token":"i","logprob":-0.2}]},"finish_reason":"stop"}]}`,
	)
	got := aggregateSSEStream(raw, logprobClientIntent{})
	require.NotContains(t, string(got), `"top_logprobs"`)
	require.NotContains(t, string(got), `"token":`)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(got, &resp))
	ch := resp["choices"].([]any)[0].(map[string]any)
	require.Nil(t, ch["logprobs"], "F10: OpenAI-shaped null when no client logprobs")
	require.Equal(t, "Hi", ch["message"].(map[string]any)["content"])
}

func TestAggregateSSEStream_EmptiesTopLogprobsWhenNotRequested(t *testing.T) {
	raw := sseData(
		`{"id":"c","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"content":"H"},"logprobs":{"content":[{"token":"H","logprob":-0.1,"top_logprobs":[{"token":"X","logprob":-1.0}]}]},"finish_reason":"stop"}]}`,
	)
	got := aggregateSSEStream(raw, logprobClientIntent{keepLogprobs: true, keepTopLogprobs: false})
	var resp map[string]any
	require.NoError(t, json.Unmarshal(got, &resp))
	entry := resp["choices"].([]any)[0].(map[string]any)["logprobs"].(map[string]any)["content"].([]any)[0].(map[string]any)
	require.Equal(t, "H", entry["token"])
	top, ok := entry["top_logprobs"].([]any)
	require.True(t, ok)
	require.Empty(t, top)
}

func TestAggregateSSEStream_PassthroughStripsInternalFields(t *testing.T) {
	payload := `{"id":"cmpl-1","object":"chat.completion","created":9,"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"Hi"},"finish_reason":"stop","logprobs":null}],"prompt_token_ids":[1,2,3],"usage":{"prompt_tokens":1,"completion_tokens":1}}`
	raw := []byte("data: " + payload + "\n\n")
	got := aggregateSSEStream(raw, logprobClientIntent{})
	require.NotContains(t, string(got), "prompt_token_ids")
	var resp map[string]any
	require.NoError(t, json.Unmarshal(got, &resp))
	require.Equal(t, "Hi", resp["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)["content"])
	_, hasPromptIDs := resp["prompt_token_ids"]
	require.False(t, hasPromptIDs)
}

func TestAggregateSSEStream_FoldStripsTokenIDs(t *testing.T) {
	raw := sseData(
		`{"id":"c","object":"chat.completion.chunk","created":1,"model":"m","prompt_token_ids":[9],"choices":[{"index":0,"delta":{"role":"assistant","content":"x","token_ids":[1]},"finish_reason":"stop"}]}`,
	)
	got := aggregateSSEStream(raw, logprobClientIntent{})
	require.NotContains(t, string(got), "token_ids")
	require.NotContains(t, string(got), "prompt_token_ids")
}

func TestAggregateSSEStream_StopReason(t *testing.T) {
	raw := sseData(
		`{"id":"c","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"content":"x"},"finish_reason":null,"stop_reason":null}]}`,
		`{"id":"c","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop","stop_reason":"end_turn"}]}`,
	)
	got := aggregateSSEStream(raw, logprobClientIntent{})
	var resp map[string]any
	require.NoError(t, json.Unmarshal(got, &resp))
	ch := resp["choices"].([]any)[0].(map[string]any)
	require.Equal(t, "stop", ch["finish_reason"])
	require.Equal(t, "end_turn", ch["stop_reason"])
}

func TestAggregateSSEStream_PassthroughChatCompletion(t *testing.T) {
	payload := `{"id":"cmpl-1","object":"chat.completion","created":9,"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"Hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`
	raw := []byte("data: " + payload + "\n\n")
	got := aggregateSSEStream(raw, logprobClientIntent{})
	require.JSONEq(t, payload, string(got))
}

func TestAggregateSSEStream_PassthroughBareJSON(t *testing.T) {
	payload := `{"id":"cmpl-1","object":"chat.completion","created":9,"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"Hi"},"finish_reason":"stop"}]}`
	got := aggregateSSEStream([]byte(payload), logprobClientIntent{})
	require.JSONEq(t, payload, string(got))
}

func TestAggregateSSEStream_EventsEnvelope(t *testing.T) {
	env, err := json.Marshal(map[string]any{
		"events": []string{
			`data: {"id":"c","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":"Y"},"finish_reason":null}]}`,
			`data: {"id":"c","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"content":"es"},"finish_reason":"stop"}]}`,
		},
	})
	require.NoError(t, err)
	got := aggregateSSEStream(env, logprobClientIntent{})
	var resp map[string]any
	require.NoError(t, json.Unmarshal(got, &resp))
	require.Equal(t, "Yes", resp["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)["content"])
}

func TestAggregateSSEStream_HostErrorPassthrough(t *testing.T) {
	payload := `{"error":{"message":"bad request","type":"BadRequestError","code":400}}`
	raw := []byte("data: " + payload + "\n\n")
	got := aggregateSSEStream(raw, logprobClientIntent{})
	require.JSONEq(t, payload, string(got))
}

func TestAggregateSSEStream_HostErrorAfterContent(t *testing.T) {
	errPayload := `{"error":{"message":"upstream failed","type":"InternalServerError","code":500}}`
	raw := sseData(
		`{"id":"c","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"content":"partial"},"finish_reason":null}]}`,
		errPayload,
	)
	got := aggregateSSEStream(raw, logprobClientIntent{})
	require.JSONEq(t, errPayload, string(got))
}

func TestAggregateSSEStream_HostErrorAfterTerminalFinish(t *testing.T) {
	// Trailing error-shaped noise after a completed answer must not discard it.
	before := aggregateDroppedTrailingErrorTotal.Load()
	errPayload := `{"error":{"message":"upstream failed","type":"InternalServerError","code":500}}`
	raw := sseData(
		`{"id":"c","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"content":"hello"},"finish_reason":null}]}`,
		`{"id":"c","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		errPayload,
	)
	got := aggregateSSEStream(raw, logprobClientIntent{})
	var resp map[string]any
	require.NoError(t, json.Unmarshal(got, &resp))
	require.Equal(t, "chat.completion", resp["object"])
	choices := resp["choices"].([]any)
	require.Len(t, choices, 1)
	ch0 := choices[0].(map[string]any)
	require.Equal(t, "stop", ch0["finish_reason"])
	msg := ch0["message"].(map[string]any)
	require.Equal(t, "hello", msg["content"])
	require.Equal(t, before+1, aggregateDroppedTrailingErrorTotal.Load())
}

func TestAggregateSSEStream_UsageNullThenError(t *testing.T) {
	// vLLM include_usage emits "usage":null on content chunks; that must not
	// mark the stream usable and swallow a following error (F2).
	errPayload := `{"error":{"message":"boom","type":"InternalServerError","code":500}}`
	raw := sseData(
		`{"id":"c","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"content":"x"},"finish_reason":null}],"usage":null}`,
		errPayload,
	)
	got := aggregateSSEStream(raw, logprobClientIntent{})
	require.JSONEq(t, errPayload, string(got))
}

func TestAggregateSSEStream_UsageNullOnlyThenError(t *testing.T) {
	errPayload := `{"error":{"message":"boom","type":"InternalServerError","code":500}}`
	raw := sseData(
		`{"id":"c","object":"chat.completion.chunk","created":1,"model":"m","choices":[],"usage":null}`,
		errPayload,
	)
	got := aggregateSSEStream(raw, logprobClientIntent{})
	require.JSONEq(t, errPayload, string(got))
}

func TestAggregateSSEStream_TruncatedNoDone(t *testing.T) {
	raw := []byte(`data: {"id":"c","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"content":"partial"},"finish_reason":null}]}` + "\n\n")
	got := aggregateSSEStream(raw, logprobClientIntent{})
	var resp map[string]any
	require.NoError(t, json.Unmarshal(got, &resp))
	require.Equal(t, "partial", resp["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)["content"])
	require.Nil(t, resp["choices"].([]any)[0].(map[string]any)["finish_reason"])
}

func TestAggregateSSEStream_NoUsableData(t *testing.T) {
	require.JSONEq(t, noResponseDataJSON, string(aggregateSSEStream(nil, logprobClientIntent{})))
	require.JSONEq(t, noResponseDataJSON, string(aggregateSSEStream([]byte("data: [DONE]\n\n"), logprobClientIntent{})))
	require.JSONEq(t, noResponseDataJSON, string(aggregateSSEStream([]byte(":\n\n"), logprobClientIntent{})))
}

func TestAggregateSSEStream_DefaultAssistantRole(t *testing.T) {
	raw := sseData(
		`{"id":"c","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"content":"x"},"finish_reason":"stop"}]}`,
	)
	got := aggregateSSEStream(raw, logprobClientIntent{})
	var resp map[string]any
	require.NoError(t, json.Unmarshal(got, &resp))
	require.Equal(t, "assistant", resp["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)["role"])
}

func TestAggregateSSEStream_SystemFingerprintFromFirst(t *testing.T) {
	raw := sseData(
		`{"id":"c","object":"chat.completion.chunk","created":1,"model":"m","system_fingerprint":"fp1","choices":[{"index":0,"delta":{"content":"a"},"finish_reason":null}]}`,
		`{"id":"c","object":"chat.completion.chunk","created":1,"model":"m","system_fingerprint":"fp2","choices":[{"index":0,"delta":{"content":"b"},"finish_reason":"stop"}]}`,
	)
	got := aggregateSSEStream(raw, logprobClientIntent{})
	var resp map[string]any
	require.NoError(t, json.Unmarshal(got, &resp))
	require.Equal(t, "fp1", resp["system_fingerprint"])
	require.Equal(t, "ab", resp["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)["content"])
}

func TestAggregateSSEStream_LateSystemFingerprintIndependent(t *testing.T) {
	// F10: each top-level meta field is first-writer independently — a fingerprint
	// that appears only after the first content chunk must still be kept.
	raw := sseData(
		`{"id":"c","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"content":"a"},"finish_reason":null}]}`,
		`{"id":"c","object":"chat.completion.chunk","created":1,"model":"m","system_fingerprint":"fp-late","choices":[{"index":0,"delta":{"content":"b"},"finish_reason":"stop"}]}`,
	)
	got := aggregateSSEStream(raw, logprobClientIntent{})
	var resp map[string]any
	require.NoError(t, json.Unmarshal(got, &resp))
	require.Equal(t, "fp-late", resp["system_fingerprint"])
	require.Equal(t, "ab", resp["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)["content"])
}

func TestAggregateSSEStream_EmitsNullLogprobsWhenNoneArrived(t *testing.T) {
	raw := sseData(
		`{"id":"c","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"content":"x"},"finish_reason":"stop"}]}`,
	)
	got := aggregateSSEStream(raw, logprobClientIntent{})
	require.Contains(t, string(got), `"logprobs":null`)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(got, &resp))
	require.Nil(t, resp["choices"].([]any)[0].(map[string]any)["logprobs"])
}

func TestDecodeChoiceForFold_SkipsLogprobsWhenNotRequested(t *testing.T) {
	chRaw := json.RawMessage(`{"index":0,"delta":{"content":"Hi"},"logprobs":{"content":[{"token":"Hi","logprob":-0.1,"top_logprobs":[{"token":"Hi","logprob":-0.1},{"token":"Hey","logprob":-1.5}]}]},"finish_reason":null}`)

	without, lpDrop, err := decodeChoiceForFold(chRaw, logprobClientIntent{})
	require.NoError(t, err)
	_, hasLP := without["logprobs"]
	require.False(t, hasLP, "F11: logprobs must not be unmarshaled for no-logprob clients")
	require.Empty(t, lpDrop)
	require.Equal(t, "Hi", without["delta"].(map[string]any)["content"])

	with, lpRaw, err := decodeChoiceForFold(chRaw, logprobClientIntent{keepLogprobs: true, keepTopLogprobs: true})
	require.NoError(t, err)
	_, hasLP = with["logprobs"]
	require.False(t, hasLP, "logprobs stay as raw sidecar, not in the choice map")
	content, extras, err := parseLogprobsContentRaw(lpRaw)
	require.NoError(t, err)
	require.Empty(t, extras)
	require.Len(t, content, 1)
	var entry map[string]any
	require.NoError(t, json.Unmarshal(content[0], &entry))
	require.Len(t, entry["top_logprobs"].([]any), 2)
}

func TestAggregateSSEStream_NoLogprobClientLowerAllocs(t *testing.T) {
	// Forced top_logprobs:5 trees are large; skipping decode (F11) must allocate less
	// than the keepLogprobs path on the same payload.
	entry := `{"token":"x","logprob":-0.1,"top_logprobs":[` +
		`{"token":"a","logprob":-0.1},{"token":"b","logprob":-0.2},{"token":"c","logprob":-0.3},` +
		`{"token":"d","logprob":-0.4},{"token":"e","logprob":-0.5}]}`
	var chunks []string
	for i := 0; i < 20; i++ {
		chunks = append(chunks, `{"id":"c","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"content":"x"},"logprobs":{"content":[`+entry+`]},"finish_reason":null}]}`)
	}
	chunks = append(chunks, `{"id":"c","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`)
	raw := sseData(chunks...)

	dropAllocs := testing.AllocsPerRun(20, func() {
		_ = aggregateSSEStream(raw, logprobClientIntent{})
	})
	keepAllocs := testing.AllocsPerRun(20, func() {
		_ = aggregateSSEStream(raw, logprobClientIntent{keepLogprobs: true, keepTopLogprobs: true})
	})
	require.Less(t, dropAllocs, keepAllocs,
		"skipping logprobs decode should allocate less (drop=%.0f keep=%.0f)", dropAllocs, keepAllocs)
}

func TestAggregateSSEStream_DeltaReasoningConcat(t *testing.T) {
	raw := sseData(
		`{"id":"c","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"role":"assistant","reasoning":"step"},"finish_reason":null}]}`,
		`{"id":"c","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"reasoning":" two","content":"ok"},"finish_reason":"stop"}]}`,
	)
	got := aggregateSSEStream(raw, logprobClientIntent{})
	var resp map[string]any
	require.NoError(t, json.Unmarshal(got, &resp))
	msg := resp["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)
	require.Equal(t, "step two", msg["reasoning"])
	require.Equal(t, "ok", msg["content"])
}

func TestAggregateSSEStream_UnknownMessageAndChoiceKeys(t *testing.T) {
	raw := sseData(
		`{"id":"c","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":"x","annotations":[{"type":"url","url":"https://example.com"}]},"matched_stop":"\n","finish_reason":null}]}`,
		`{"id":"c","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"content":"y","annotations":[{"type":"url","url":"https://other.example"}]},"matched_stop":".","finish_reason":"stop"}]}`,
	)
	got := aggregateSSEStream(raw, logprobClientIntent{})
	var resp map[string]any
	require.NoError(t, json.Unmarshal(got, &resp))
	ch := resp["choices"].([]any)[0].(map[string]any)
	msg := ch["message"].(map[string]any)
	require.Equal(t, "xy", msg["content"])
	// Unknown message key: first-writer-wins (not concatenated).
	ann := msg["annotations"].([]any)
	require.Len(t, ann, 1)
	require.Equal(t, "https://example.com", ann[0].(map[string]any)["url"])
	// Unknown choice key: first-writer-wins.
	require.Equal(t, "\n", ch["matched_stop"])
}

func TestAggregateSSEStream_ReasoningDetailsArray(t *testing.T) {
	raw := sseData(
		`{"id":"c","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":"final","reasoning_details":[{"type":"text","text":"step 1"},{"type":"text","text":"step 2"}]},"finish_reason":"stop"}]}`,
	)
	got := aggregateSSEStream(raw, logprobClientIntent{})
	var resp map[string]any
	require.NoError(t, json.Unmarshal(got, &resp))
	msg := resp["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)
	require.Equal(t, "final", msg["content"])
	details, ok := msg["reasoning_details"].([]any)
	require.True(t, ok)
	require.Len(t, details, 2)
	require.Equal(t, "step 1", details[0].(map[string]any)["text"])
	require.Equal(t, "step 2", details[1].(map[string]any)["text"])
}

func TestAggregateSSEStream_TopLevelUnknownKeyFirstWriter(t *testing.T) {
	raw := sseData(
		`{"id":"c","object":"chat.completion.chunk","created":1,"model":"m","service_tier":"default","choices":[{"index":0,"delta":{"content":"a"},"finish_reason":null}]}`,
		`{"id":"c","object":"chat.completion.chunk","created":1,"model":"m","service_tier":"scale","choices":[{"index":0,"delta":{"content":"b"},"finish_reason":"stop"}]}`,
	)
	got := aggregateSSEStream(raw, logprobClientIntent{})
	var resp map[string]any
	require.NoError(t, json.Unmarshal(got, &resp))
	require.Equal(t, "default", resp["service_tier"])
	require.Equal(t, "ab", resp["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)["content"])
}

func TestAggregateSSEStream_ErrorSubstringInContentIsNotHostError(t *testing.T) {
	// F8's byte gate looks for a quoted "error" token, so prose mentioning the
	// word skips the parse entirely; anything that slips through must still fold.
	raw := sseData(
		`{"id":"c","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"content":"error: not an error object"},"finish_reason":"stop"}]}`,
	)
	got := aggregateSSEStream(raw, logprobClientIntent{})
	var resp map[string]any
	require.NoError(t, json.Unmarshal(got, &resp))
	require.Equal(t, "error: not an error object", resp["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)["content"])
}

func TestIsHostErrorPayload_ByteGate(t *testing.T) {
	require.False(t, isHostErrorPayload([]byte(`{"id":"c","choices":[]}`)))
	require.True(t, isHostErrorPayload([]byte(`{"error":{"message":"x"}}`)))
	require.True(t, isHostErrorPayload([]byte(`{"object":"error","message":"x","type":"t"}`)))
	require.False(t, isHostErrorPayload([]byte(`{"choices":[{"delta":{"content":"error: hi"}}]}`)))
	// Quoted marker: a chunk that merely talks about errors must not reach the
	// error-shaped unmarshal at all.
	require.False(t, bytes.Contains([]byte(`{"delta":{"content":"handle the error case"}}`), sseErrorKeyMarker))
}

func TestAggregateSSEStream_ChoiceIndexFanoutBounded(t *testing.T) {
	before := aggregateDroppedChoiceFanoutTotal.Load()
	var parts []string
	// Hostile fan-out: indexes outside the safety window, then the real choice 0.
	for i := aggregateMaxChoices; i < aggregateMaxChoices+200; i++ {
		parts = append(parts, fmt.Sprintf(
			`{"id":"c","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":%d,"delta":{"content":"x"},"finish_reason":null}]}`, i))
	}
	parts = append(parts,
		`{"id":"c","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":null}]}`,
		`{"id":"c","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
	)
	got := aggregateSSEStream(sseData(parts...), logprobClientIntent{})
	var resp map[string]any
	require.NoError(t, json.Unmarshal(got, &resp))
	choices := resp["choices"].([]any)
	require.Len(t, choices, 1)
	ch0 := choices[0].(map[string]any)
	require.Equal(t, float64(0), ch0["index"])
	require.Equal(t, "ok", ch0["message"].(map[string]any)["content"])
	require.GreaterOrEqual(t, aggregateDroppedChoiceFanoutTotal.Load(), before+200)
}

func TestAggregateSSEStream_ToolCallFanoutBounded(t *testing.T) {
	before := aggregateDroppedToolCallFanoutTotal.Load()
	var tcs []string
	for i := 0; i < aggregateMaxToolCallsPerChoice+40; i++ {
		tcs = append(tcs, fmt.Sprintf(`{"index":%d,"id":"c%d","type":"function","function":{"name":"f%d","arguments":"{}"}}`, i, i, i))
	}
	payload := fmt.Sprintf(
		`{"id":"c","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"tool_calls":[%s]},"finish_reason":"tool_calls"}]}`,
		strings.Join(tcs, ","))
	got := aggregateSSEStream(sseData(payload), logprobClientIntent{})
	var resp map[string]any
	require.NoError(t, json.Unmarshal(got, &resp))
	msg := resp["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)
	toolCalls := msg["tool_calls"].([]any)
	require.Len(t, toolCalls, aggregateMaxToolCallsPerChoice)
	require.GreaterOrEqual(t, aggregateDroppedToolCallFanoutTotal.Load(), before+40)
}

func TestAggregateSSEStream_ExtrasFanoutBounded(t *testing.T) {
	before := aggregateDroppedExtrasFanoutTotal.Load()

	// Top-level: flood non-reserved keys past the cap; reserved id/model/created
	// must still land.
	topExtras := make([]string, 0, aggregateMaxExtrasKeys+30)
	for i := 0; i < aggregateMaxExtrasKeys+30; i++ {
		topExtras = append(topExtras, fmt.Sprintf(`"x_extra_%d":%d`, i, i))
	}
	// Choice extras: unknown keys on the choice object (not delta/message).
	choiceExtras := make([]string, 0, aggregateMaxExtrasKeys+20)
	for i := 0; i < aggregateMaxExtrasKeys+20; i++ {
		choiceExtras = append(choiceExtras, fmt.Sprintf(`"c_extra_%d":%d`, i, i))
	}
	payload := fmt.Sprintf(
		`{"id":"kept-id","object":"chat.completion.chunk","created":7,"model":"kept-model",%s,"choices":[{"index":0,"delta":{"content":"ok"},%s,"finish_reason":"stop"}]}`,
		strings.Join(topExtras, ","),
		strings.Join(choiceExtras, ","),
	)
	got := aggregateSSEStream(sseData(payload), logprobClientIntent{})
	var resp map[string]any
	require.NoError(t, json.Unmarshal(got, &resp))
	require.Equal(t, "kept-id", resp["id"])
	require.Equal(t, "kept-model", resp["model"])
	require.Equal(t, float64(7), resp["created"])

	extraTop := 0
	for k := range resp {
		switch k {
		case "id", "object", "created", "model", "choices", "usage", "system_fingerprint", "service_tier":
			continue
		}
		if strings.HasPrefix(k, "x_extra_") {
			extraTop++
		}
	}
	require.Equal(t, aggregateMaxExtrasKeys, extraTop)

	ch := resp["choices"].([]any)[0].(map[string]any)
	extraChoice := 0
	for k := range ch {
		if strings.HasPrefix(k, "c_extra_") {
			extraChoice++
		}
	}
	require.Equal(t, aggregateMaxExtrasKeys, extraChoice)
	require.GreaterOrEqual(t, aggregateDroppedExtrasFanoutTotal.Load(), before+50)
}

func TestAggregateSSEStreamReader_MatchesBytesPath(t *testing.T) {
	cases := []struct {
		name string
		raw  []byte
	}{
		{
			name: "multi_chunk_sse",
			raw: sseData(
				`{"id":"c","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":"hi"},"finish_reason":null}]}`,
				`{"id":"c","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"content":"!"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`,
			),
		},
		{
			name: "empty_sse",
			raw:  []byte("data: [DONE]\n\n"),
		},
		{
			name: "host_error_only",
			raw:  []byte("data: {\"error\":{\"message\":\"bad\",\"type\":\"BadRequestError\",\"code\":400}}\n\n"),
		},
		{
			name: "single_chat_completion_sse",
			raw:  []byte("data: {\"id\":\"cmpl-1\",\"object\":\"chat.completion\",\"created\":9,\"model\":\"m\",\"choices\":[{\"index\":0,\"message\":{\"role\":\"assistant\",\"content\":\"Hi\"},\"finish_reason\":\"stop\"}]}\n\n"),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fromBytes := aggregateSSEStream(tc.raw, logprobClientIntent{})
			fromReader := aggregateSSEStreamReader(bytes.NewReader(tc.raw), logprobClientIntent{})
			require.JSONEq(t, string(fromBytes), string(fromReader))
		})
	}
}

func TestAggregateSSEStream_EventsEnvelopeBytesOnlyFallback(t *testing.T) {
	// Reader has no envelope support; bytes path falls back after the shared
	// SSE scan finds nothing (R5).
	env, err := json.Marshal(map[string]any{
		"events": []string{
			`data: {"id":"c","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":"Y"},"finish_reason":null}]}`,
			`data: {"id":"c","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"content":"es"},"finish_reason":"stop"}]}`,
		},
	})
	require.NoError(t, err)

	fromBytes := aggregateSSEStream(env, logprobClientIntent{})
	var resp map[string]any
	require.NoError(t, json.Unmarshal(fromBytes, &resp))
	require.Equal(t, "Yes", resp["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)["content"])

	fromReader := aggregateSSEStreamReader(bytes.NewReader(env), logprobClientIntent{})
	require.JSONEq(t, noResponseDataJSON, string(fromReader))
}

func TestAggregateSSEStream_BareJSONBytesOnlyFallback(t *testing.T) {
	payload := `{"id":"cmpl-1","object":"chat.completion","created":9,"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"Hi"},"finish_reason":"stop"}]}`
	fromBytes := aggregateSSEStream([]byte(payload), logprobClientIntent{})
	require.JSONEq(t, payload, string(fromBytes))

	fromReader := aggregateSSEStreamReader(bytes.NewReader([]byte(payload)), logprobClientIntent{})
	// Bare JSON is one scanner line without a data: prefix, so the reader sees
	// no payloads; bytes path recovers via the bare-JSON fallback.
	require.JSONEq(t, noResponseDataJSON, string(fromReader))
}

type errAfterReader struct {
	r   io.Reader
	err error
}

func (e errAfterReader) Read(p []byte) (int, error) {
	n, err := e.r.Read(p)
	if err == io.EOF {
		if n > 0 {
			return n, nil
		}
		return 0, e.err
	}
	return n, err
}

func TestAggregateSSEStreamReader_ReadErrorKeepsUsableFold(t *testing.T) {
	raw := sseData(
		`{"id":"c","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":"kept"},"finish_reason":"stop"}]}`,
	)
	got := aggregateSSEStreamReader(errAfterReader{r: bytes.NewReader(raw), err: errors.New("spool read failed")}, logprobClientIntent{})
	var resp map[string]any
	require.NoError(t, json.Unmarshal(got, &resp))
	require.Equal(t, "chat.completion", resp["object"])
	require.Equal(t, "kept", resp["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)["content"])
	require.NotContains(t, string(got), "aggregate stream read failed")
}

func TestAggregateSSEStreamReader_ReadErrorWithoutFoldIsDistinguishable(t *testing.T) {
	got := aggregateSSEStreamReader(errAfterReader{r: bytes.NewReader(nil), err: errors.New("spool read failed")}, logprobClientIntent{})
	require.JSONEq(t, aggregateStreamReadFailedJSON, string(got))
	require.False(t, isAggregateNoResponseData(got))
}

func TestAggregateSSEStreamReader_OversizeLineIsReadFailure(t *testing.T) {
	// Shared scanner cap is DefaultMaxSSEEventBytes+64; an oversize data line
	// must not silently produce a success body (R5 line-cap equivalence).
	maxLine := transport.DefaultMaxSSEEventBytes + 64
	huge := bytes.Repeat([]byte("a"), maxLine+1)
	raw := append([]byte("data: {\"id\":\"c\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\""), huge...)
	raw = append(raw, []byte("\"},\"finish_reason\":\"stop\"}]}\n\n")...)

	fromReader := aggregateSSEStreamReader(bytes.NewReader(raw), logprobClientIntent{})
	fromBytes := aggregateSSEStream(raw, logprobClientIntent{})
	require.JSONEq(t, aggregateStreamReadFailedJSON, string(fromReader))
	require.JSONEq(t, string(fromReader), string(fromBytes))
}

func TestAggregateSSEStream_FoldRAMBudgetRejectsHugeLogprobs(t *testing.T) {
	prevMem, prevResp := currentAggregateByteLimits()
	prevDir := currentAggregateSpoolDir()
	t.Cleanup(func() {
		setAggregateByteLimitsForTest(prevMem, prevResp)
		setAggregateSpoolDir(prevDir)
	})
	setAggregateSpoolDir("")
	setAggregateByteLimitsForTest(4<<10, prevResp) // 4 KiB fold RAM

	// Each entry is ~200 bytes; many chunks exceed the fold RAM budget.
	entry := `{"token":"xxxxxxxx","logprob":-0.1,"top_logprobs":[{"token":"xxxxxxxx","logprob":-0.1},{"token":"yyyyyyyy","logprob":-1.0}]}`
	var chunks []string
	for i := 0; i < 80; i++ {
		chunks = append(chunks, `{"id":"c","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"content":"x"},"logprobs":{"content":[`+entry+`]},"finish_reason":null}]}`)
	}
	chunks = append(chunks, `{"id":"c","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`)
	got := aggregateSSEStream(sseData(chunks...), logprobClientIntent{keepLogprobs: true, keepTopLogprobs: true})
	require.True(t, isAggregateFoldTooLargePayload(got), "got %s", got)
	require.JSONEq(t, aggregateFoldTooLargeJSON, string(got))
}

func TestAggregateSSEStream_LogprobsSpillUnderDiskBudget(t *testing.T) {
	prevMem, prevDisk := currentAggregateByteLimits()
	prevDir := currentAggregateSpoolDir()
	dir := t.TempDir()
	t.Cleanup(func() {
		setAggregateByteLimitsForTest(prevMem, prevDisk)
		setAggregateSpoolDir(prevDir)
	})
	setAggregateSpoolDir(dir)
	setAggregateByteLimitsForTest(2<<10, 1<<20) // tiny RAM — forces spill for logprobs

	entry := `{"token":"tok","logprob":-0.1,"top_logprobs":[{"token":"tok","logprob":-0.1}]}`
	var chunks []string
	for i := 0; i < 200; i++ {
		chunks = append(chunks, `{"id":"c","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"content":"x"},"logprobs":{"content":[`+entry+`]},"finish_reason":null}]}`)
	}
	chunks = append(chunks, `{"id":"c","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`)
	got := aggregateSSEStream(sseData(chunks...), logprobClientIntent{keepLogprobs: true, keepTopLogprobs: true})
	require.False(t, isAggregateFoldTooLargePayload(got), "spool should absorb logprobs: %s", got)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(got, &resp))
	lp := resp["choices"].([]any)[0].(map[string]any)["logprobs"].(map[string]any)["content"].([]any)
	require.Len(t, lp, 200)
	// Spill files must be cleaned up after fold.
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	for _, e := range entries {
		require.False(t, strings.HasPrefix(e.Name(), "agg-lp-"), "leaked logprobs spill %s", e.Name())
	}
}

func TestAggregateSSEStream_LogprobsExtrasKeyCap(t *testing.T) {
	prev := aggregateDroppedExtrasFanoutTotal.Load()
	extras := make([]string, 0, aggregateMaxExtrasKeys+20)
	for i := 0; i < aggregateMaxExtrasKeys+20; i++ {
		extras = append(extras, fmt.Sprintf(`"lp_extra_%d":%d`, i, i))
	}
	payload := `{"id":"c","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"content":"x"},"logprobs":{"content":[{"token":"x","logprob":-0.1}],` +
		strings.Join(extras, ",") + `},"finish_reason":"stop"}]}`
	got := aggregateSSEStream(sseData(payload), logprobClientIntent{keepLogprobs: true, keepTopLogprobs: true})
	var resp map[string]any
	require.NoError(t, json.Unmarshal(got, &resp))
	lp := resp["choices"].([]any)[0].(map[string]any)["logprobs"].(map[string]any)
	extraCount := 0
	for k := range lp {
		if strings.HasPrefix(k, "lp_extra_") {
			extraCount++
		}
	}
	require.Equal(t, aggregateMaxExtrasKeys, extraCount)
	require.Greater(t, aggregateDroppedExtrasFanoutTotal.Load(), prev)
}

func TestAggregateSSEStream_NoLogprobClientDoesNotCreateStore(t *testing.T) {
	raw := sseData(
		`{"id":"c","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"content":"Hi"},"logprobs":{"content":[{"token":"Hi","logprob":-0.1}]},"finish_reason":"stop"}]}`,
	)
	got := aggregateSSEStream(raw, logprobClientIntent{})
	require.NotContains(t, string(got), `"token"`)
	require.Contains(t, string(got), `"logprobs":null`)
}

// json.RawMessage keeps the upstream's original whitespace, so a pretty-printed
// entry arriving through the (not line-framed) events envelope used to break
// NDJSON framing and turn the whole answer into "no response data".
func TestAggregateSSEStream_LogprobsEntryWithEmbeddedNewline(t *testing.T) {
	chunk := "{\"id\":\"c\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"m\"," +
		"\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"},\"logprobs\":{\"content\":[{\n\"token\":\"hi\",\n\"logprob\":-0.5}]},\"finish_reason\":\"stop\"}]}"
	env, err := json.Marshal(map[string]any{"events": []string{"data: " + chunk}})
	require.NoError(t, err)

	got := aggregateSSEStream(env, logprobClientIntent{keepLogprobs: true, keepTopLogprobs: true})
	var resp map[string]any
	require.NoError(t, json.Unmarshal(got, &resp), "aggregate output must be valid JSON: %s", got)
	choice := resp["choices"].([]any)[0].(map[string]any)
	entries := choice["logprobs"].(map[string]any)["content"].([]any)
	require.Len(t, entries, 1)
	require.Equal(t, "hi", entries[0].(map[string]any)["token"])
}

// {"content":null,"refusal":[…]} is OpenAI's refusal shape: the siblings must
// survive even though content[] never arrives.
func TestAggregateSSEStream_LogprobsSiblingsWithoutContent(t *testing.T) {
	raw := sseData(
		`{"id":"c","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"refusal":"no"},"logprobs":{"content":null,"refusal":[{"token":"no","logprob":-0.1}]},"finish_reason":"stop"}]}`,
	)
	got := aggregateSSEStream(raw, logprobClientIntent{keepLogprobs: true, keepTopLogprobs: true})
	var resp map[string]any
	require.NoError(t, json.Unmarshal(got, &resp))
	lp := resp["choices"].([]any)[0].(map[string]any)["logprobs"].(map[string]any)
	require.Len(t, lp["refusal"], 1)
	require.Nil(t, lp["content"])
}

// A logprobs response that fits the RAM budget must not touch the spool at all.
func TestCompletionFolder_SmallLogprobsStayInMemory(t *testing.T) {
	dir := t.TempDir()
	prevDir := currentAggregateSpoolDir()
	t.Cleanup(func() { setAggregateSpoolDir(prevDir) })
	setAggregateSpoolDir(dir)

	f := newCompletionFolder(logprobClientIntent{keepLogprobs: true, keepTopLogprobs: true})
	defer f.close()
	_, early := f.ingest([]byte(`{"id":"c","choices":[{"index":0,"delta":{"content":"hi"},"logprobs":{"content":[{"token":"hi","logprob":-0.5}]}}]}`))
	require.False(t, early)
	require.False(t, f.choices[0].lp.spilled, "tiny logprobs payload must not spill")
	require.Zero(t, f.diskBytes)
	require.Greater(t, f.ramBytes, int64(0))

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Empty(t, entries)
}

// A spool fault at emit time must not be reported as "the host sent no
// logprobs" — it is a read failure like any other (R4/R5).
func TestCompletionFolder_LogprobsSpillReadFailureIsSurfaced(t *testing.T) {
	prevMem, prevResp := currentAggregateByteLimits()
	prevDir := currentAggregateSpoolDir()
	t.Cleanup(func() {
		setAggregateByteLimitsForTest(prevMem, prevResp)
		setAggregateSpoolDir(prevDir)
	})
	setAggregateSpoolDir(t.TempDir())
	setAggregateByteLimitsForTest(1, prevResp) // no RAM headroom: spill immediately

	f := newCompletionFolder(logprobClientIntent{keepLogprobs: true, keepTopLogprobs: true})
	defer f.close()
	_, early := f.ingest([]byte(`{"id":"c","choices":[{"index":0,"logprobs":{"content":[{"token":"hi","logprob":-0.5}]}}]}`))
	require.False(t, early)
	require.True(t, f.choices[0].lp.spilled)

	require.NoError(t, f.choices[0].lp.corruptForTest()) // reads now fail

	out, ok := f.result()
	require.True(t, ok)
	require.JSONEq(t, aggregateStreamReadFailedJSON, string(out))
}

// The spool budget is per request, not per choice: two choices spilling
// logprobs share one disk ceiling.
func TestAggregateSSEStream_LogprobsSpillTakesSpoolSlot(t *testing.T) {
	prevMem, prevResp := currentAggregateByteLimits()
	prevDir := currentAggregateSpoolDir()
	t.Cleanup(func() {
		setAggregateByteLimitsForTest(prevMem, prevResp)
		setAggregateSpoolDir(prevDir)
		resetAggregateSpoolSlots(defaultAggregateMaxConcurrentSpools)
	})
	dir := t.TempDir()
	setAggregateSpoolDir(dir)
	resetAggregateSpoolSlots(1)
	setAggregateByteLimitsForTest(1, 1<<20)

	// Occupy the only spool slot with a body buffer spill.
	body := newAggregateResponseBuffer()
	defer func() { _ = body.Close() }()
	_, err := body.Write([]byte("xx"))
	require.NoError(t, err)
	require.True(t, body.Spilled())

	got := aggregateSSEStream(sseData(
		`{"id":"c","choices":[{"index":0,"delta":{"content":"hi"},"logprobs":{"content":[{"token":"hi","logprob":-0.5}]},"finish_reason":"stop"}]}`,
	), logprobClientIntent{keepLogprobs: true, keepTopLogprobs: true})
	require.True(t, isAggregateFoldTooLargePayload(got),
		"logprobs spill must take a spool slot and fail the fold when none remain: %s", got)
}

func TestCompletionFolder_LogprobsSpillIsAnonymous(t *testing.T) {
	prevMem, prevResp := currentAggregateByteLimits()
	prevDir := currentAggregateSpoolDir()
	t.Cleanup(func() {
		setAggregateByteLimitsForTest(prevMem, prevResp)
		setAggregateSpoolDir(prevDir)
	})
	dir := t.TempDir()
	setAggregateSpoolDir(dir)
	setAggregateByteLimitsForTest(1, 1<<20)

	f := newCompletionFolder(logprobClientIntent{keepLogprobs: true, keepTopLogprobs: true})
	defer f.close()
	_, early := f.ingest([]byte(`{"id":"c","choices":[{"index":0,"logprobs":{"content":[{"token":"hi","logprob":-0.5}]}}]}`))
	require.False(t, early)
	require.True(t, f.choices[0].lp.spilled)
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	for _, e := range entries {
		require.False(t, strings.HasPrefix(e.Name(), "agg-lp-"), "named logprobs spill %s", e.Name())
		require.False(t, strings.Contains(e.Name(), ".ndjson"), "visible ndjson spill %s", e.Name())
	}
}

func TestAggregateSSEStream_LogprobsDiskBudgetIsPerRequest(t *testing.T) {
	prevMem, prevResp := currentAggregateByteLimits()
	prevDir := currentAggregateSpoolDir()
	t.Cleanup(func() {
		setAggregateByteLimitsForTest(prevMem, prevResp)
		setAggregateSpoolDir(prevDir)
	})
	setAggregateSpoolDir(t.TempDir())
	setAggregateByteLimitsForTest(1, 4<<10) // no RAM, 4 KiB of spool for the whole fold

	// ~31 spool bytes per entry: 100 per choice stays under the cap alone,
	// 200 across both choices does not.
	entry := `{"token":"tok","logprob":-0.1}`
	var chunks []string
	for i := 0; i < 100; i++ {
		for _, idx := range []int{0, 1} {
			chunks = append(chunks, fmt.Sprintf(
				`{"id":"c","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":%d,"delta":{},"logprobs":{"content":[%s]}}]}`,
				idx, entry))
		}
	}
	got := aggregateSSEStream(sseData(chunks...), logprobClientIntent{keepLogprobs: true, keepTopLogprobs: true})
	require.True(t, isAggregateFoldTooLargePayload(got),
		"combined spill across choices must hit the shared disk cap: %s", got)
}
