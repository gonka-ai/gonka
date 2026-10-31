package filters

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func sseStream(events ...string) []byte {
	var stream []byte
	for _, event := range events {
		stream = append(stream, "data: "...)
		stream = append(stream, event...)
		stream = append(stream, "\n\n"...)
	}
	return append(stream, "data: [DONE]\n\n"...)
}

// Test flow:
//  1. Encode each test case's SSE chunk payloads into an event stream via `sseStream`.
//  2. Assemble the stream with assembleSSEBody.
//  3. Assert the result matches the case's expected completion body.
//  4. Vary the case across delta merging, a trailing usage-only chunk, reasoning content, split tool-call arguments, per-token logprobs, choice ordering, and an already-whole completion.
func TestAssembleSSEBodyMergesDeltas(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name   string
		events []string
		want   string
	}{
		{
			name: "content accumulates into one message",
			events: []string{
				`{"id":"a","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"He"},"finish_reason":null}]}`,
				`{"id":"a","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"llo"},"finish_reason":null}]}`,
				`{"id":"a","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
			},
			want: `{"choices":[{"finish_reason":"stop","index":0,"message":{"content":"Hello","role":"assistant"}}],"id":"a","object":"chat.completion"}`,
		},
		{
			name: "the final usage chunk carries no choice of its own",
			events: []string{
				`{"id":"a","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"hi"},"finish_reason":"stop"}]}`,
				`{"id":"a","object":"chat.completion.chunk","choices":[],"usage":{"completion_tokens":2,"prompt_tokens":5,"total_tokens":7}}`,
			},
			want: `{"choices":[{"finish_reason":"stop","index":0,"message":{"content":"hi","role":"assistant"}}],"id":"a","object":"chat.completion","usage":{"completion_tokens":2,"prompt_tokens":5,"total_tokens":7}}`,
		},
		{
			name: "reasoning accumulates beside content",
			events: []string{
				`{"choices":[{"index":0,"delta":{"reasoning_content":"th"}}]}`,
				`{"choices":[{"index":0,"delta":{"reasoning_content":"ink","content":"out"}}]}`,
			},
			want: `{"choices":[{"index":0,"message":{"content":"out","reasoning_content":"think","role":"assistant"}}],"object":"chat.completion"}`,
		},
		{
			name: "two tool calls keep their own arguments",
			events: []string{
				`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"lookup","arguments":"{\"a\":"}}]}}]}`,
				`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"id":"c2","function":{"name":"send","arguments":"{\"b\":"}}]}}]}`,
				`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"1}"}}]}}]}`,
				`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"function":{"arguments":"2}"}}]}}]}`,
			},
			want: `{"choices":[{"index":0,"message":{"content":null,"role":"assistant","tool_calls":[{"function":{"arguments":"{\"a\":1}","name":"lookup"},"id":"c1"},{"function":{"arguments":"{\"b\":2}","name":"send"},"id":"c2"}]}}],"object":"chat.completion"}`,
		},
		{
			name: "every token's logprobs survive, not just the last chunk's",
			events: []string{
				`{"choices":[{"index":0,"delta":{"content":"a"},"logprobs":{"content":[{"token":"a","logprob":-0.1}]}}]}`,
				`{"choices":[{"index":0,"delta":{"content":"b"},"logprobs":{"content":[{"token":"b","logprob":-0.2}]}}]}`,
			},
			want: `{"choices":[{"index":0,"logprobs":{"content":[{"logprob":-0.1,"token":"a"},{"logprob":-0.2,"token":"b"}]},"message":{"content":"ab","role":"assistant"}}],"object":"chat.completion"}`,
		},
		{
			name: "choices come back in index order",
			events: []string{
				`{"choices":[{"index":1,"delta":{"content":"second"}}]}`,
				`{"choices":[{"index":0,"delta":{"content":"first"}}]}`,
			},
			want: `{"choices":[{"index":0,"message":{"content":"first","role":"assistant"}},{"index":1,"message":{"content":"second","role":"assistant"}}],"object":"chat.completion"}`,
		},
		{
			name: "a host answering with a whole completion is left alone",
			events: []string{
				`{"id":"a","object":"chat.completion","choices":[{"index":0,"message":{"content":"whole"},"finish_reason":"stop"}]}`,
			},
			want: `{"id":"a","object":"chat.completion","choices":[{"index":0,"message":{"content":"whole"},"finish_reason":"stop"}]}`,
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if got := string(assembleSSEBody(sseStream(testCase.events...))); got != testCase.want {
				t.Fatalf("assembled\n got: %s\nwant: %s", got, testCase.want)
			}
		})
	}
}

// Test flow:
//  1. Send one SSE event with two choices, each carrying its own logprobs content.
//  2. Assemble the stream with assembleSSEBody.
//  3. Assert both choices keep their own logprobs in the assembled completion.
func TestAssembleSSEBodyKeepsLogprobsOfEveryChoice(t *testing.T) {
	t.Parallel()
	assembled := string(assembleSSEBody(sseStream(
		`{"choices":[{"index":0,"delta":{"content":"a"},"logprobs":{"content":[{"token":"a"}]}},{"index":1,"delta":{"content":"x"},"logprobs":{"content":[{"token":"x"}]}}]}`,
	)))
	want := `{"choices":[{"index":0,"logprobs":{"content":[{"token":"a"}]},"message":{"content":"a","role":"assistant"}},{"index":1,"logprobs":{"content":[{"token":"x"}]},"message":{"content":"x","role":"assistant"}}],"object":"chat.completion"}`
	if assembled != want {
		t.Fatalf("assembled\n got: %s\nwant: %s", assembled, want)
	}
}

// Test flow:
//  1. Send two deltas for one tool call, each repeating its id and type, with the name and split arguments spread across them.
//  2. Assemble the stream with assembleSSEBody.
//  3. Assert the merged tool call keeps its id, type and name whole alongside the joined arguments.
func TestAssembleSSEBodyKeepsIdentityFieldsWhole(t *testing.T) {
	t.Parallel()
	assembled := string(assembleSSEBody(sseStream(
		`{"choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"\"Paris\"}"}}]}}]}`,
	)))
	want := `{"choices":[{"index":0,"message":{"content":null,"role":"assistant","tool_calls":[{"function":{"arguments":"{\"city\":\"Paris\"}","name":"get_weather"},"id":"call_1","type":"function"}]}}],"object":"chat.completion"}`
	if assembled != want {
		t.Fatalf("assembled\n got: %s\nwant: %s", assembled, want)
	}
}

// Test flow:
//  1. Build a raw SSE payload whose JSON is split mid-object across two consecutive `data:` lines.
//  2. Assemble the stream with assembleSSEBody.
//  3. Assert the split lines are rejoined into one delta before assembly.
func TestAssembleSSEBodyJoinsSplitDataLines(t *testing.T) {
	t.Parallel()
	split := []byte("data: {\"id\":\"a\",\"choices\":[{\"index\":0,\"delta\":\ndata: {\"content\":\"hi\"}}]}\n\ndata: [DONE]\n\n")

	assembled := string(assembleSSEBody(split))

	want := `{"choices":[{"index":0,"message":{"content":"hi","role":"assistant"}}],"id":"a","object":"chat.completion"}`
	if assembled != want {
		t.Fatalf("assembled\n got: %s\nwant: %s", assembled, want)
	}
}

// Test flow:
//  1. Send three choice deltas out of order: index 1, an index-less delta, then index 0.
//  2. Assemble the stream with assembleSSEBody.
//  3. Assert indexed choices sort by index first, and the index-less choice, which compares equal to every other, lands last.
func TestAssembleSSEBodyOrdersChoicesWithAnIndexFirst(t *testing.T) {
	t.Parallel()
	assembled := string(assembleSSEBody(sseStream(
		`{"choices":[{"index":1,"delta":{"content":"one"}}]}`,
		`{"choices":[{"delta":{"content":"none"}}]}`,
		`{"choices":[{"index":0,"delta":{"content":"zero"}}]}`,
	)))
	want := `{"choices":[{"index":0,"message":{"content":"zero","role":"assistant"}},{"index":1,"message":{"content":"one","role":"assistant"}},{"message":{"content":"none","role":"assistant"}}],"object":"chat.completion"}`
	if assembled != want {
		t.Fatalf("assembled\n got: %s\nwant: %s", assembled, want)
	}
}

// Test flow:
//  1. Send maxIndexedElements+8 delta events, each carrying a distinct choice index.
//  2. Assemble the stream with assembleSSEBody.
//  3. Decode the assembled choices.
//  4. Assert the merge is bounded at maxIndexedElements.
func TestAssembleSSEBodyBoundsTheIndexedMerge(t *testing.T) {
	t.Parallel()
	events := make([]string, 0, maxIndexedElements+8)
	for index := range maxIndexedElements + 8 {
		events = append(events, fmt.Sprintf(`{"choices":[{"index":%d,"delta":{"content":"x"}}]}`, index))
	}

	assembled := assembleSSEBody(sseStream(events...))

	var decoded struct {
		Choices []json.RawMessage `json:"choices"`
	}
	if err := json.Unmarshal(assembled, &decoded); err != nil {
		t.Fatalf("unmarshalling the assembled body: %v", err)
	}
	if len(decoded.Choices) != maxIndexedElements {
		t.Fatalf("choices = %d, want the merge bounded at %d", len(decoded.Choices), maxIndexedElements)
	}
}

// Test flow:
//  1. Send maxTopLevelFields+8 events, each adding one invented top-level field alongside the choices delta.
//  2. Assemble the stream with assembleSSEBody.
//  3. Decode the assembled body into a field map.
//  4. Assert the merged key count stays bounded at maxTopLevelFields, plus the object field the assembler always writes regardless of what the host sent.
//  5. Assert the choices field survives the bound.
func TestAssembleSSEBodyBoundsTheKeySet(t *testing.T) {
	t.Parallel()
	events := make([]string, 0, maxTopLevelFields+8)
	for index := range maxTopLevelFields + 8 {
		events = append(events, fmt.Sprintf(`{"invented%d":"x","choices":[{"index":0,"delta":{"content":"y"}}]}`, index))
	}

	assembled := assembleSSEBody(sseStream(events...))

	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(assembled, &decoded); err != nil {
		t.Fatalf("unmarshalling the assembled body: %v", err)
	}
	if len(decoded) > maxTopLevelFields+1 {
		t.Fatalf("fields = %d, want the key set bounded at %d", len(decoded), maxTopLevelFields)
	}
	if _, held := decoded["choices"]; !held {
		t.Error("the bound dropped choices, which the reply cannot do without")
	}
}

// Test flow:
//  1. Send two chunks whose choice each carries its own token_ids array alongside the content delta.
//  2. Assemble the stream with assembleSSEBody.
//  3. Assert the assembled choice concatenates the per-chunk token_ids, unlike every other choice-level field, which merges by delta.
func TestAssembleSSEBodyAccumulatesTokenIds(t *testing.T) {
	t.Parallel()
	assembled := string(assembleSSEBody(sseStream(
		`{"choices":[{"index":0,"delta":{"content":"a"},"token_ids":[11]}]}`,
		`{"choices":[{"index":0,"delta":{"content":"b"},"token_ids":[22]}]}`,
	)))
	want := `{"choices":[{"index":0,"message":{"content":"ab","role":"assistant"},"token_ids":[11,22]}],"object":"chat.completion"}`
	if assembled != want {
		t.Fatalf("assembled\n got: %s\nwant: %s", assembled, want)
	}
}

// Test flow:
//  1. Send maxAssembledEvents+16 identical content-delta events.
//  2. Assemble the stream with assembleSSEBody.
//  3. Assert the assembler refuses with TruncatedResponseBody instead of returning a silently truncated answer.
func TestAssembleSSEBodyRefusesRatherThanTruncate(t *testing.T) {
	t.Parallel()
	events := make([]string, 0, maxAssembledEvents+16)
	for range maxAssembledEvents + 16 {
		events = append(events, `{"choices":[{"index":0,"delta":{"content":"x"}}]}`)
	}

	assembled := assembleSSEBody(sseStream(events...))

	if !bytes.Equal(assembled, TruncatedResponseBody) {
		t.Fatalf("assembled = %s, want the truncation error: a prefix is indistinguishable from a complete answer", assembled)
	}
}

// Test flow:
//  1. Send two content deltas followed by a trailing whole chat.completion chunk carrying different content.
//  2. Assemble the stream with assembleSSEBody.
//  3. Decode the assembled choices.
//  4. Assert the folded deltas survive and the trailing completion's content does not win.
func TestAssembleSSEBodyKeepsFoldedDeltasWhenACompletionFollows(t *testing.T) {
	t.Parallel()
	assembled := assembleSSEBody(sseStream(
		`{"choices":[{"index":0,"delta":{"content":"hello "}}]}`,
		`{"choices":[{"index":0,"delta":{"content":"world"}}]}`,
		`{"object":"chat.completion","choices":[{"index":0,"message":{"content":"discarded"}}]}`,
	))

	var decoded struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(assembled, &decoded); err != nil {
		t.Fatalf("unmarshalling the assembled body: %v", err)
	}
	if len(decoded.Choices) == 0 {
		t.Fatal("no choices survived")
	}
	if got := decoded.Choices[0].Message.Content; got != "hello world" {
		t.Fatalf("content = %q, want the folded deltas: a trailing completion must not discard them", got)
	}
}

// Test flow:
//  1. Send one SSE event whose payload is already a complete chat.completion object.
//  2. Assemble the stream with assembleSSEBody.
//  3. Assert the payload passes through unchanged.
func TestAssembleSSEBodyPassesASoleCompletionThrough(t *testing.T) {
	t.Parallel()
	payload := `{"object":"chat.completion","choices":[{"index":0,"message":{"content":"whole"}}]}`

	assembled := assembleSSEBody(sseStream(payload))

	if string(assembled) != payload {
		t.Fatalf("assembled = %s, want the already-complete payload unchanged", assembled)
	}
}

// Test flow:
//  1. Build a raw `data:` line carrying two JSON objects back to back.
//  2. Assemble the stream with assembleSSEBody.
//  3. Assert the smuggled second object causes the payload to be rejected as NoResponseDataBody.
func TestAssembleSSEBodyRejectsAPayloadCarryingTwoObjects(t *testing.T) {
	t.Parallel()

	assembled := string(assembleSSEBody([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"real\"}}]} {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"smuggled\"}}]}\n\ndata: [DONE]\n\n")))

	if assembled != string(NoResponseDataBody) {
		t.Fatalf("assembled = %s, want the payload rejected", assembled)
	}
}

// Test flow:
//  1. Send three tool-call argument deltas where the last chunk resends the whole assembled argument string instead of a fragment, as some upstream versions do on the finish chunk.
//  2. Assemble the stream with assembleSSEBody.
//  3. Assert the assembled arguments equal the resent whole string rather than the fragments doubled up with it.
func TestAssembleSSEBodyDoesNotDoubleArgumentsResentWhole(t *testing.T) {
	t.Parallel()
	assembled := string(assembleSSEBody(sseStream(
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"ci"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"ty\":"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"city\":\"Paris\"}"}}]}}]}`,
	)))
	want := `{"choices":[{"index":0,"message":{"content":null,"role":"assistant","tool_calls":[{"function":{"arguments":"{\"city\":\"Paris\"}"}}]}}],"object":"chat.completion"}`
	if assembled != want {
		t.Fatalf("assembled\n got: %s\nwant: %s", assembled, want)
	}
}

// Test flow:
//  1. Load the recorded Kimi reply fixture, which carries thinking tags inline in content, an empty tool_calls array on every chunk, and a trailing usage event.
//  2. Assemble the fixture stream with assembleSSEBody.
//  3. Assert the assembled completion matches the exact expected JSON.
func TestAssembleSSEBodyOnARecordedKimiReply(t *testing.T) {
	t.Parallel()
	stream, err := os.ReadFile(filepath.Join("testdata", "sse", "kimi_thinking_stream.sse"))
	if err != nil {
		t.Fatalf("reading the fixture: %v", err)
	}

	assembled := string(assembleSSEBody(stream))

	want := `{"choices":[{"finish_reason":"length","index":0,"message":{"content":" I'll </think> examining","role":"assistant"}}],"created":1786114584,"id":"devshard-46055-1453","model":"moonshotai/Kimi-K2.6","object":"chat.completion","usage":{"completion_tokens":16,"prompt_tokens":46,"total_tokens":62}}`
	if assembled != want {
		t.Fatalf("assembled\n got: %s\nwant: %s", assembled, want)
	}
}

// Test flow:
//  1. Send two tool-call argument deltas with an empty string, followed by two deltas that build up the real arguments.
//  2. Assemble the stream with assembleSSEBody.
//  3. Assert the empty leading chunks leave the accumulator empty and the later chunks still grow it into the full arguments.
func TestAssembleSSEBodyGrowsArgumentsThatStartEmpty(t *testing.T) {
	t.Parallel()
	assembled := string(assembleSSEBody(sseStream(
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":""}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":""}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"city\":"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"Paris\"}"}}]}}]}`,
	)))
	want := `{"choices":[{"index":0,"message":{"content":null,"role":"assistant","tool_calls":[{"function":{"arguments":"{\"city\":\"Paris\"}"}}]}}],"object":"chat.completion"}`
	if assembled != want {
		t.Fatalf("assembled\n got: %s\nwant: %s", assembled, want)
	}
}
