package engine

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Test flow:
//  1. Build a chunk with a 49,019-element `prompt_token_ids` array (over 200KB) alongside a small delta, `finish_reason`, `token_ids` and `usage`.
//  2. Compute its `chunkHead`.
//  3. Assert the head keeps the delta, finish_reason and usage fields but elides both the `prompt_token_ids` and `token_ids` arrays.
//  4. Assert the head stays within the max empty-chunk-logged budget plus the elided placeholder.
func TestTheHeadElidesTheIdsThatMakeAContentlessChunkLarge(t *testing.T) {
	ids := make([]string, 0, 49_019)
	for id := range 49_019 {
		ids = append(ids, fmt.Sprint(id))
	}
	chunk := fmt.Appendf(nil,
		`data: {"choices":[{"delta":{"content":""},"finish_reason":"length","token_ids":[7,8,9]}],"prompt_token_ids":[%s],"usage":{"completion_tokens":0,"prompt_tokens":49019}}`,
		strings.Join(ids, ","))
	require.Greater(t, len(chunk), 200_000, "the fixture has to be the size that made the field useless")

	head := chunkHead(chunk)

	require.Contains(t, head, `"delta":{"content":""}`, "the delta is what the head exists to show")
	require.Contains(t, head, `"finish_reason":"length"`)
	require.Contains(t, head, `"prompt_token_ids":[...]`)
	require.Contains(t, head, `"token_ids":[...]`)
	require.Contains(t, head, `"completion_tokens":0`, "the usage sits past the ids and must survive them")
	require.LessOrEqual(t, len(head), maxEmptyChunkLogged+len(elidedValue))
}

// Test flow:
//  1. Build a chunk whose bulky `prompt_logprobs` array contains a nested object.
//  2. Compute its `chunkHead`.
//  3. Assert the head elides the whole array and resumes correctly at the following `usage` field.
func TestTheHeadResumesAfterANestedBulkyValue(t *testing.T) {
	chunk := []byte(`data: {"prompt_logprobs":[{"7":{"logprob":-0.5,"top":[1,2]}},null],"usage":{"prompt_tokens":3}}`)

	head := chunkHead(chunk)

	require.Equal(t, `data: {"prompt_logprobs":[...],"usage":{"prompt_tokens":3}}`, head)
}

// Test flow:
//  1. Build a chunk whose `logprobs` field is `null` rather than an array.
//  2. Compute its `chunkHead`.
//  3. Assert the head keeps the field unchanged.
func TestTheHeadKeepsABulkyFieldThatIsNotAnArray(t *testing.T) {
	chunk := []byte(`data: {"choices":[{"logprobs":null,"delta":{"content":"hi"}}]}`)

	head := chunkHead(chunk)

	require.Equal(t, `data: {"choices":[{"logprobs":null,"delta":{"content":"hi"}}]}`, head)
}

// Test flow:
//  1. Build a chunk whose `token_ids` array contains a string with an escaped bracket-like character.
//  2. Compute its `chunkHead`.
//  3. Assert the elision skips past the string's bracket without cutting the array short.
func TestTheHeadSkipsPastBracketsInsideStrings(t *testing.T) {
	chunk := []byte(`data: {"token_ids":[{"text":"]}\"still inside"}],"usage":{"prompt_tokens":1}}`)

	head := chunkHead(chunk)

	require.Equal(t, `data: {"token_ids":[...],"usage":{"prompt_tokens":1}}`, head)
}

// Test flow:
//  1. Compute `chunkHead` for a `[DONE]` terminator chunk.
//  2. Assert the returned head is empty.
func TestTheTerminatorIsNeverKeptAsAHead(t *testing.T) {
	require.Empty(t, chunkHead([]byte("data: [DONE]\n\n")))
}
