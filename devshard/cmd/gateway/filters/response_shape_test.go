package filters

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

func chunkWithChoices(count int) string {
	choices := make([]string, count)
	for index := range choices {
		choices[index] = fmt.Sprintf(`{"index":%d,"delta":{"content":"x"},"logprobs":null}`, index)
	}
	return `{"object":"chat.completion.chunk","choices":[` + strings.Join(choices, ",") + `]}`
}

func chunkWithNodes(count int) string {
	return `{"object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"x","extra":[` + strings.Repeat("0,", count-1) + `0]},"logprobs":null}]}`
}

func chunkWithDeltaMembers(count int) string {
	members := make([]string, count)
	for index := range members {
		members[index] = fmt.Sprintf(`"k%d":0`, index)
	}
	return `{"object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"x",` + strings.Join(members, ",") + `},"logprobs":null}]}`
}

func completionWithLogprobTokens(count int) string {
	tokens := make([]string, count)
	for index := range tokens {
		tokens[index] = `{"token":"a","logprob":-0.1,"bytes":[97],"top_logprobs":[{"token":"a","logprob":-0.1,"bytes":[97]},{"token":"b","logprob":-2,"bytes":[98]}]}`
	}
	return `{"object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"a"},"logprobs":{"content":[` + strings.Join(tokens, ",") + `]},"finish_reason":"stop"}]}`
}

func withUsage(chunk string) string {
	return strings.TrimSuffix(chunk, "}") + `,"usage":{"prompt_tokens":1}}`
}

func sseEvent(payload string) string {
	return "data: " + payload + "\n\n"
}

// Test flow:
//  1. Rewrite one streamed chunk per shape limit: too many choices, too many nodes, too many members in one object; each carries a `usage` the client did not ask for, so the rewrite decodes it.
//  2. Rewrite the same chunks at the limit.
//  3. Assert each chunk past a limit is reported malformed and forwards nothing, and each one at the limit is forwarded.
func TestRewriteRefusesAChunkPastTheResponseShapeLimits(t *testing.T) {
	tests := []struct {
		name   string
		within string
		past   string
	}{
		{name: "choices", within: chunkWithChoices(maxIndexedElements), past: chunkWithChoices(maxIndexedElements + 1)},
		{name: "nodes", within: chunkWithNodes(maxStreamedChunkNodes / 2), past: chunkWithNodes(maxStreamedChunkNodes + 1)},
		{name: "object members", within: chunkWithDeltaMembers(maxResponseObjectMembers - 1), past: chunkWithDeltaMembers(maxResponseObjectMembers)},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			within, withinMalformed, _ := rewriteEvent([]byte(sseEvent(withUsage(testCase.within))), LogprobIntent{}, false, 0)
			past, pastMalformed, _ := rewriteEvent([]byte(sseEvent(withUsage(testCase.past))), LogprobIntent{}, false, 0)

			if withinMalformed || len(within) == 0 {
				t.Errorf("a chunk at the limit: malformed = %t, %d bytes, want forwarded", withinMalformed, len(within))
			}
			if !pastMalformed || len(past) != 0 {
				t.Errorf("a chunk past the limit: malformed = %t, %d bytes, want refused with nothing forwarded", pastMalformed, len(past))
			}
		})
	}
}

// Test flow:
//  1. Fold and assemble a stream of one ordinary chunk followed by a chunk with more choices than the limit.
//  2. Fold and assemble the same stream with that chunk at the limit instead.
//  3. Assert the stream past the limit answers TruncatedResponseBody both ways, and the one at the limit does not.
func TestFoldAndAssembleTruncateAtAChunkPastTheResponseShapeLimits(t *testing.T) {
	ordinary := sseEvent(`{"object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"hi"}}]}`)
	past := []byte(ordinary + sseEvent(chunkWithChoices(maxIndexedElements+1)) + "data: [DONE]\n\n")
	within := []byte(ordinary + sseEvent(chunkWithChoices(maxIndexedElements)) + "data: [DONE]\n\n")

	if got := foldChunked(t, past, LogprobIntent{}, len(past)); !bytes.Equal(got, TruncatedResponseBody) {
		t.Errorf("fold past the limit = %.80q, want TruncatedResponseBody", got)
	}
	if got := assembleSSEBody(past); !bytes.Equal(got, TruncatedResponseBody) {
		t.Errorf("assemble past the limit = %.80q, want TruncatedResponseBody", got)
	}
	if got := foldChunked(t, within, LogprobIntent{}, len(within)); bytes.Equal(got, TruncatedResponseBody) {
		t.Error("fold at the limit answered TruncatedResponseBody, want the folded completion")
	}
	if got := assembleSSEBody(within); bytes.Equal(got, TruncatedResponseBody) {
		t.Error("assemble at the limit answered TruncatedResponseBody, want the assembled completion")
	}
}

// Test flow:
//  1. Build a whole completion whose logprobs carry more nodes than the streamed-chunk limit allows.
//  2. Fold it as one SSE event and strip it as a plain body, for a client that asked for logprobs.
//  3. Assert both keep the completion: a whole completion is held to the member and choice limits only.
func TestAWholeCompletionIsNotHeldToTheStreamedChunkNodeLimit(t *testing.T) {
	completion := completionWithLogprobTokens(maxStreamedChunkNodes / 16)
	intent := LogprobIntent{Keep: true, KeepTop: true}

	folded := foldChunked(t, []byte(sseEvent(completion)+"data: [DONE]\n\n"), intent, len(completion))
	stripped := stripResponseBody([]byte(completion), intent, 0)

	if bytes.Equal(folded, TruncatedResponseBody) || !bytes.Contains(folded, []byte(`"top_logprobs"`)) {
		t.Errorf("folded completion = %.80q, want it kept with its logprobs", folded)
	}
	if bytes.Equal(stripped, TruncatedResponseBody) || !bytes.Contains(stripped, []byte(`"top_logprobs"`)) {
		t.Errorf("stripped completion = %.80q, want it kept with its logprobs", stripped)
	}
}
