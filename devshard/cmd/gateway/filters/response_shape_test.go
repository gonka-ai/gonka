package filters

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"devshard/internal/jsonshape"
)

func chunkWithChoices(count int) string {
	choices := make([]string, count)
	for index := range choices {
		choices[index] = fmt.Sprintf(`{"index":%d,"delta":{"content":"x"},"logprobs":null}`, index)
	}
	return `{"object":"chat.completion.chunk","choices":[` + strings.Join(choices, ",") + `]}`
}

func chunkWithExtraElements(count int) string {
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

func sseEvent(payload string) []byte {
	return []byte("data: " + payload + "\n\n")
}

func nodesOf(t *testing.T, payload string) int {
	t.Helper()
	stats, err := jsonshape.Check([]byte(payload), jsonshape.Limits{})
	if err != nil {
		t.Fatalf("jsonshape.Check(): %v", err)
	}
	return stats.Nodes
}

func chunkAtNodes(t *testing.T, nodes int) string {
	t.Helper()
	overhead := nodesOf(t, withUsage(chunkWithExtraElements(1))) - 1
	return chunkWithExtraElements(nodes - overhead)
}

// Test flow:
//  1. Rewrite one streamed chunk per shape limit at the limit and one past it: choices, nodes, members of one object; each carries a `usage` the client did not ask for, so the rewrite decodes it.
//  2. Assert each chunk past a limit is reported malformed and forwards nothing, and each one at the limit is forwarded.
func TestRewriteRefusesAChunkPastTheResponseShapeLimits(t *testing.T) {
	tests := []struct {
		name   string
		within string
		past   string
	}{
		{name: "choices", within: chunkWithChoices(maxIndexedElements), past: chunkWithChoices(maxIndexedElements + 1)},
		{name: "nodes", within: chunkAtNodes(t, maxStreamedChunkNodes), past: chunkAtNodes(t, maxStreamedChunkNodes+1)},
		{name: "object members", within: chunkWithDeltaMembers(maxResponseObjectMembers - 1), past: chunkWithDeltaMembers(maxResponseObjectMembers)},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			within, withinMalformed, _ := rewriteEvent(sseEvent(withUsage(testCase.within)), LogprobIntent{}, false, 0)
			past, pastMalformed, _ := rewriteEvent(sseEvent(withUsage(testCase.past)), LogprobIntent{}, false, 0)

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
//  1. Rewrite an event whose payload is an array of objects carrying `token_ids` and a `message`, one past the node limit and one with a single element.
//  2. Assert the array past the limit is refused rather than forwarded with its `token_ids`, while the single element is still stripped.
func TestRewriteRefusesANonObjectPayloadPastTheResponseShapeLimits(t *testing.T) {
	element := `{"token_ids":[1],"message":{"content":"x"}}`
	past := "[" + strings.Repeat(element+",", maxStreamedChunkNodes/4) + element + "]"

	rewritten, malformed, _ := rewriteEvent(sseEvent(past), LogprobIntent{}, false, 0)
	single, singleMalformed, _ := rewriteEvent(sseEvent("["+element+"]"), LogprobIntent{}, false, 0)

	if !malformed || len(rewritten) != 0 {
		t.Errorf("an array past the limit: malformed = %t, %d bytes, want refused with nothing forwarded", malformed, len(rewritten))
	}
	if singleMalformed || bytes.Contains(single, []byte("token_ids")) {
		t.Errorf("a single-element array = %q (malformed %t), want it forwarded without token_ids", single, singleMalformed)
	}
}

// Test flow:
//  1. Build a chunk past the node limit, padded as sparsely as a completion, that also names itself `chat.completion` under a key differing only in case.
//  2. Build the same padding under the exact key, and a payload labelled `chat.completion` whose padding is denser than any completion: an array of empty arrays.
//  3. Assert the case variant and the dense payload are past the response shape limits, and the sparse completion under the exact key is not.
func TestTheCompletionExemptionNeedsTheExactKeyAndACompletionsDensity(t *testing.T) {
	sparsePadding := strings.Repeat(`"padding",`, maxStreamedChunkNodes) + `"padding"`
	densePadding := strings.Repeat("[],", maxStreamedChunkNodes) + "[]"
	caseVariant := `{"object":"chat.completion.chunk","choices":[],"pad":[` + sparsePadding + `],"OBJECT":"chat.completion"}`
	exactKey := `{"object":"chat.completion","choices":[],"pad":[` + sparsePadding + `]}`
	dense := `{"object":"chat.completion","choices":[],"pad":[` + densePadding + `]}`

	if !responseShapeExceeded([]byte(caseVariant)) {
		t.Error("a chunk naming itself chat.completion under OBJECT took the completion exemption")
	}
	if responseShapeExceeded([]byte(exactKey)) {
		t.Error("a sparse chat.completion under the exact key was held to the streamed-chunk node limit")
	}
	if !responseShapeExceeded([]byte(dense)) {
		t.Error("a chat.completion denser than minCompletionBytesPerNode took the completion exemption")
	}
}

// Test flow:
//  1. Fold and assemble a stream of one ordinary chunk followed by a chunk with more choices than the limit.
//  2. Fold and assemble the same stream with that chunk at the limit instead.
//  3. Assert the stream past the limit answers TruncatedResponseBody both ways, and the one at the limit does not.
func TestFoldAndAssembleTruncateAtAChunkPastTheResponseShapeLimits(t *testing.T) {
	ordinary := `{"object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"hi"}}]}`
	past := sseStream(ordinary, chunkWithChoices(maxIndexedElements+1))
	within := sseStream(ordinary, chunkWithChoices(maxIndexedElements))

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
//  3. Assert both keep the completion: a whole completion's node limit scales with its size.
func TestAWholeCompletionIsNotHeldToTheStreamedChunkNodeLimit(t *testing.T) {
	completion := completionWithLogprobTokens(maxStreamedChunkNodes / 8)
	if nodesOf(t, completion) <= maxStreamedChunkNodes {
		t.Fatalf("precondition: the completion carries %d nodes, want more than %d", nodesOf(t, completion), maxStreamedChunkNodes)
	}
	intent := LogprobIntent{Keep: true, KeepTop: true}

	folded := foldChunked(t, sseStream(completion), intent, len(completion))
	stripped := stripResponseBody([]byte(completion), intent, 0)

	if bytes.Equal(folded, TruncatedResponseBody) || !bytes.Contains(folded, []byte(`"top_logprobs"`)) {
		t.Errorf("folded completion = %.80q, want it kept with its logprobs", folded)
	}
	if bytes.Equal(stripped, TruncatedResponseBody) || !bytes.Contains(stripped, []byte(`"top_logprobs"`)) {
		t.Errorf("stripped completion = %.80q, want it kept with its logprobs", stripped)
	}
}
